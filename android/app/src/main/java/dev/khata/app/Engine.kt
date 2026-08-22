package dev.khata.app

import android.content.Context
import android.util.Log
// gomobile treats -javapkg as a PREFIX and appends the Go package name,
// so `-javapkg=dev.khata.engine` on package `mobile` generates
// dev.khata.engine.mobile.*, not dev.khata.engine.*.
import dev.khata.engine.mobile.Khata
import dev.khata.engine.mobile.Mobile
import kotlinx.serialization.json.Json
import java.io.File

/**
 * Single point of contact with the Go engine.
 *
 * The engine holds an open append-only file and an in-memory index, so exactly
 * one instance must exist per process. Both capture paths — the notification
 * listener and the SMS receiver — run in this same process but on different
 * threads, hence the synchronised accessor and the mutex around ingest.
 */
object Engine {

    private const val TAG = "khata.engine"
    private const val LEDGER = "ledger.jsonl"
    private const val SYNTHESIZED_PACK = "synthesized.json"

    val json = Json {
        ignoreUnknownKeys = true
        coerceInputValues = true
    }

    @Volatile
    private var instance: Khata? = null
    private val lock = Any()

    /**
     * Returns the engine, opening it on first use.
     *
     * The ledger lives in the app's private files directory, which is readable
     * only by this app's UID and is excluded from backup by the manifest.
     */
    fun get(context: Context): Khata = instance ?: synchronized(lock) {
        instance ?: open(context.applicationContext).also { instance = it }
    }

    private fun open(context: Context): Khata {
        val ledger = File(context.filesDir, LEDGER).absolutePath
        val pack = File(context.filesDir, SYNTHESIZED_PACK)

        // A rule pack compiled from this user's own corrections is layered over
        // the builtin one when present. Its absence is the normal case.
        return if (pack.exists()) {
            Mobile.openWithRulePack(ledger, pack.absolutePath)
        } else {
            Mobile.open(ledger)
        }
    }

    /**
     * Feeds one captured message to the engine.
     *
     * Called from a binder thread inside the notification listener, so it must
     * not block. The work behind it is a regex pass, a rule scan and one
     * appended line; the mutex only serialises the file write.
     */
    fun ingest(
        context: Context,
        sourcePackage: String,
        sender: String,
        title: String,
        body: String,
        receivedAtMillis: Long,
    ): IngestResult? = synchronized(lock) {
        return try {
            val raw = get(context).ingest(sourcePackage, sender, title, body, receivedAtMillis)
            json.decodeFromString<IngestResult>(raw)
        } catch (t: Throwable) {
            // A capture failure must never crash the listener: Android will
            // disable a notification listener that keeps throwing, and the user
            // would silently stop getting any transactions at all.
            Log.e(TAG, "ingest failed for $sourcePackage", t)
            null
        }
    }

    fun monthStates(context: Context, year: Int, month: Int): List<EnvelopeState> = runCatching {
        json.decodeFromString<List<EnvelopeState>>(get(context).monthStates(year.toLong(), month.toLong()))
    }.getOrElse {
        Log.e(TAG, "monthStates failed", it)
        emptyList()
    }

    fun transactions(context: Context, year: Int, month: Int): List<Transaction> = runCatching {
        json.decodeFromString<List<Transaction>>(get(context).transactions(year.toLong(), month.toLong()))
    }.getOrElse {
        Log.e(TAG, "transactions failed", it)
        emptyList()
    }

    fun needsReview(context: Context, limit: Int = 50): List<Transaction> = runCatching {
        json.decodeFromString<List<Transaction>>(get(context).needsReview(limit.toLong()))
    }.getOrElse {
        Log.e(TAG, "needsReview failed", it)
        emptyList()
    }

    fun categories(context: Context): List<String> = runCatching {
        json.decodeFromString<List<String>>(get(context).categories())
    }.getOrElse { emptyList() }

    fun correct(context: Context, transactionId: String, category: String): Boolean = synchronized(lock) {
        runCatching { get(context).correct(transactionId, category) }
            .onFailure { Log.e(TAG, "correct failed", it) }
            .isSuccess
    }

    fun setEnvelope(context: Context, category: String, limitMinor: Long): Boolean = synchronized(lock) {
        runCatching { get(context).setEnvelope(category, limitMinor) }
            .onFailure { Log.e(TAG, "setEnvelope failed", it) }
            .isSuccess
    }

    fun corrections(context: Context): List<Correction> = runCatching {
        json.decodeFromString<List<Correction>>(get(context).corrections())
    }.getOrElse { emptyList() }

    /** Formats minor units the way the engine does, so no screen invents its own. */
    fun money(minor: Long): String = Mobile.formatMoney(minor)

    fun ruleCount(context: Context): Int = runCatching { get(context).ruleCount().toInt() }.getOrDefault(0)

    fun transactionCount(context: Context): Int = runCatching { get(context).count().toInt() }.getOrDefault(0)

    fun version(): String = Mobile.version()
}
