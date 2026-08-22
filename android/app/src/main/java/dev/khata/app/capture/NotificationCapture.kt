package dev.khata.app.capture

import android.service.notification.NotificationListenerService
import android.service.notification.StatusBarNotification
import android.util.Log
import dev.khata.app.Engine
import dev.khata.app.notify.Notifier

/**
 * The capture surface.
 *
 * Android hands a notification listener every notification on the device, which
 * is an enormous amount of access for a budgeting app to hold. Two things keep
 * that honest. The package allowlist below means khata only ever looks at
 * notifications from banks and payment apps, and the missing INTERNET
 * permission in the manifest means whatever it does look at cannot go anywhere.
 *
 * Notifications it does not recognise are dropped here, before the text is read.
 */
class NotificationCapture : NotificationListenerService() {

    override fun onListenerConnected() {
        Log.i(TAG, "listener connected")
    }

    override fun onListenerDisconnected() {
        Log.w(TAG, "listener disconnected; the user has revoked access or the OS reclaimed us")
    }

    override fun onNotificationPosted(sbn: StatusBarNotification?) {
        val notification = sbn ?: return
        val pkg = notification.packageName ?: return

        if (!isFinancialSource(pkg)) return

        // Group summaries repeat the text of the notifications they collapse,
        // which would double-count every transaction on some launchers.
        if (notification.notification.flags and android.app.Notification.FLAG_GROUP_SUMMARY != 0) return

        val extras = notification.notification.extras ?: return
        val title = extras.getCharSequence(android.app.Notification.EXTRA_TITLE)?.toString().orEmpty()
        val text = extras.getCharSequence(android.app.Notification.EXTRA_TEXT)?.toString().orEmpty()
        // Some issuers put the useful detail only in the expanded view.
        val bigText = extras.getCharSequence(android.app.Notification.EXTRA_BIG_TEXT)?.toString().orEmpty()
        val body = if (bigText.length > text.length) bigText else text

        if (title.isBlank() && body.isBlank()) return

        // An SMS app's notifications include messages from people. Only let
        // through the ones whose sender looks like a bank's DLT header; the SMS
        // receiver handles that path properly when the user grants it.
        if (pkg in MESSAGING_PACKAGES && !looksInstitutional(title)) return

        handle(pkg, sender = pkg, title = title, body = body, whenMillis = notification.postTime)
    }

    private fun handle(pkg: String, sender: String, title: String, body: String, whenMillis: Long) {
        val result = Engine.ingest(this, pkg, sender, title, body, whenMillis) ?: return

        when {
            result.booked -> {
                val txn = result.txn ?: return
                // Only interrupt when there is an actual decision to make.
                if (txn.needsReview) {
                    Notifier.askForCategory(this, txn)
                }
                result.alerts.forEach { Notifier.budgetAlert(this, it) }
            }

            result.duplicate -> Log.d(TAG, "duplicate ignored: ${result.reason}")
            else -> Log.d(TAG, "not booked (${result.kind}): ${result.reason}")
        }
    }

    companion object {
        private const val TAG = "khata.capture"

        /**
         * SMS apps, included only so bank texts are still seen when the user has
         * not granted the SMS permission. Their notifications also carry
         * messages from people, so everything from these packages must clear
         * [looksInstitutional] before the engine sees it.
         *
         * WhatsApp is deliberately absent. It does carry payment receipts, but
         * it carries far more that is none of this app's business, and there is
         * no reliable way to tell them apart from a notification title.
         */
        private val MESSAGING_PACKAGES = setOf(
            "com.google.android.apps.messaging",
            "com.samsung.android.messaging",
        )

        /**
         * Packages khata will read notifications from.
         *
         * An allowlist rather than a blocklist: an unknown app's notifications
         * are none of khata's business, and the cost of a missing bank is one
         * pull request, while the cost of a too-broad filter is reading
         * someone's private messages.
         */
        private val ALLOWED_PACKAGES = setOf(
            // Payment apps
            "com.google.android.apps.nbu.paisa.user", // Google Pay India
            "net.one97.paytm",
            "com.phonepe.app",
            "in.amazon.mShop.android.shopping",
            "com.freecharge.android",
            "com.mobikwik_new",
            "com.dreamplug.androidapp", // CRED

            // Banks
            "com.snapwork.hdfc",
            "com.csam.icici.bank.imobile",
            "com.sbi.lotusintouch",
            "com.sbi.SBIFreedomPlus",
            "com.axis.mobile",
            "com.msf.kbank.mobile", // Kotak
            "com.bankofbaroda.mconnect",
            "com.infrasofttech.indianbank",
            "com.fss.pnbpsp",
            "com.idbibank.mpassbook",
            "com.yesbank",
            "com.rblbank.mobank",
            "com.indusind.indie",
            "com.jupiter.money",
            "money.fi.app",

        ) + MESSAGING_PACKAGES

        /**
         * Indian institutional SMS arrives from a DLT header such as
         * "AD-HDFCBK" or "AD-HDFCBK-S", never from a phone number or a contact
         * name. An SMS app puts that header in the notification title.
         */
        private val SHORT_CODE = Regex("""^[A-Z]{2}-[A-Z0-9]{3,8}(-[A-Z])?$|^[A-Z]{5,11}$""")

        fun looksInstitutional(title: String): Boolean =
            title.isNotBlank() && SHORT_CODE.matches(title.trim().uppercase())

        fun isFinancialSource(pkg: String): Boolean = pkg in ALLOWED_PACKAGES
    }
}
