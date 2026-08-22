package dev.khata.app

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Kotlin mirrors of the engine's JSON payloads.
 *
 * gomobile only bridges scalars and strings, so anything structured crosses as
 * JSON. These classes are the Kotlin half of that contract; the Go half is in
 * `core/model` and `core/engine`. Field names must match the Go struct tags
 * exactly, which is why every one is spelled out with @SerialName rather than
 * relying on a naming convention that could quietly drift.
 */

@Serializable
data class Transaction(
    @SerialName("id") val id: String,
    @SerialName("occurred_at") val occurredAt: String,
    @SerialName("amount_minor") val amountMinor: Long,
    @SerialName("currency") val currency: String = "INR",
    @SerialName("direction") val direction: String,
    @SerialName("channel") val channel: String = "unknown",
    @SerialName("merchant_raw") val merchantRaw: String = "",
    @SerialName("merchant_key") val merchantKey: String = "",
    @SerialName("account_hint") val accountHint: String = "",
    @SerialName("reference") val reference: String = "",
    @SerialName("category") val category: String = "uncategorized",
    @SerialName("category_source") val categorySource: String = "",
    @SerialName("matched_rule") val matchedRule: String = "",
    @SerialName("confidence") val confidence: Double = 0.0,
    @SerialName("needs_review") val needsReview: Boolean = false,
    @SerialName("raw_text") val rawText: String = "",
) {
    val isDebit: Boolean get() = direction == "debit"

    /** A name safe to show in a notification, falling back when extraction found none. */
    fun displayName(): String = merchantRaw.ifBlank { "Unknown merchant" }
}

@Serializable
data class EnvelopeState(
    @SerialName("category") val category: String,
    @SerialName("limit_minor") val limitMinor: Long = 0,
    @SerialName("spent_minor") val spentMinor: Long = 0,
    @SerialName("pct") val pct: Double = 0.0,
    @SerialName("projected_minor") val projectedMinor: Long = 0,
    @SerialName("status") val status: String = "ok",
    @SerialName("txn_count") val txnCount: Int = 0,
) {
    val hasBudget: Boolean get() = limitMinor > 0
}

@Serializable
data class BudgetAlert(
    @SerialName("category") val category: String,
    @SerialName("threshold") val threshold: Int,
    @SerialName("title") val title: String,
    @SerialName("body") val body: String,
    @SerialName("status") val status: String = "ok",
)

@Serializable
data class RuleDecision(
    @SerialName("category") val category: String = "",
    @SerialName("rule_id") val ruleId: String = "",
    @SerialName("source") val source: String = "",
    @SerialName("matched") val matched: Boolean = false,
)

@Serializable
data class IngestResult(
    @SerialName("booked") val booked: Boolean = false,
    @SerialName("duplicate") val duplicate: Boolean = false,
    @SerialName("reason") val reason: String = "",
    @SerialName("kind") val kind: String = "",
    @SerialName("txn") val txn: Transaction? = null,
    @SerialName("alerts") val alerts: List<BudgetAlert> = emptyList(),
    @SerialName("decision") val decision: RuleDecision = RuleDecision(),
)

@Serializable
data class Correction(
    @SerialName("merchant_raw") val merchantRaw: String = "",
    @SerialName("merchant_key") val merchantKey: String = "",
    @SerialName("channel") val channel: String = "",
    @SerialName("from_category") val fromCategory: String = "",
    @SerialName("to_category") val toCategory: String = "",
)
