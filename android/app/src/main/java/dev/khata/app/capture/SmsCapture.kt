package dev.khata.app.capture

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.provider.Telephony
import android.util.Log
import dev.khata.app.Engine
import dev.khata.app.notify.Notifier

/**
 * Fallback capture for banks that still only send SMS.
 *
 * Many Indian issuers have no app notification for a UPI debit at all, so the
 * notification listener alone would miss them. This receiver is optional: the
 * app works without the SMS permission, it just sees fewer transactions.
 *
 * Long messages arrive split into multiple PDUs. They are reassembled here
 * before parsing, because a transaction alert cut in half loses either its
 * amount or its reference number.
 */
class SmsCapture : BroadcastReceiver() {

    override fun onReceive(context: Context?, intent: Intent?) {
        val ctx = context ?: return
        if (intent?.action != Telephony.Sms.Intents.SMS_RECEIVED_ACTION) return

        val messages = Telephony.Sms.Intents.getMessagesFromIntent(intent) ?: return
        if (messages.isEmpty()) return

        val sender = messages.first().originatingAddress.orEmpty()
        if (!looksLikeAnInstitution(sender)) return

        val body = messages.joinToString(separator = "") { it.messageBody.orEmpty() }
        if (body.isBlank()) return

        val received = messages.first().timestampMillis

        // onReceive runs on the main thread, and ingest does a JNI call plus an
        // fsynced ledger write while holding a lock the notification listener
        // may already be inside. That is a disk write on the main thread and an
        // ANR risk, so it goes to a worker.
        val pending = goAsync()
        Thread {
            try {
                val result = Engine.ingest(ctx, "sms", sender, "", body, received)
                when {
                    result == null -> Unit
                    result.booked -> {
                        result.txn?.let { if (it.needsReview) Notifier.askForCategory(ctx, it) }
                        result.alerts.forEach { Notifier.budgetAlert(ctx, it) }
                    }
                    else -> Log.d(TAG, "sms not booked (${result.kind}): ${result.reason}")
                }
            } catch (t: Throwable) {
                Log.e(TAG, "sms ingest failed", t)
            } finally {
                pending.finish()
            }
        }.start()
    }

    private companion object {
        const val TAG = "khata.sms"

        /**
         * Indian institutional SMS arrives from an alphanumeric header such as
         * "AD-HDFCBK", never from a phone number. Filtering on that shape means
         * khata does not read messages from people.
         *
         * Since the 2021 DLT rollout the header usually carries a trailing
         * entity-type letter — "AD-HDFCBK-S" for service, -T, -P, -G — so the
         * suffix has to be optional. Requiring the bare form drops most real
         * bank traffic on the floor.
         */
        val SHORT_CODE = Regex("""^[A-Z]{2}-[A-Z0-9]{3,8}(-[A-Z])?$|^[A-Z]{5,11}$""")

        fun looksLikeAnInstitution(sender: String): Boolean =
            sender.isNotBlank() && SHORT_CODE.matches(sender.uppercase())
    }
}
