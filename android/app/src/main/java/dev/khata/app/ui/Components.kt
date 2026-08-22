package dev.khata.app.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.khata.app.Engine
import dev.khata.app.EnvelopeState
import dev.khata.app.Transaction

@Composable
fun SectionHeader(title: String, badge: String? = null) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(top = 12.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(title, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
        if (badge != null) {
            Badge { Text(badge) }
        }
    }
}

/**
 * A budget line with a progress bar.
 *
 * The bar is capped at full while the number below is not, so an overspend
 * reads as "over" at a glance and as "by how much" on a second look. A bar that
 * silently clamps and a number that silently clamps would both be lies.
 */
@Composable
fun BudgetRow(state: EnvelopeState, onClick: () -> Unit = {}) {
    val fraction = if (state.hasBudget) (state.spentMinor.toFloat() / state.limitMinor).coerceIn(0f, 1f) else 0f

    Column(
        Modifier.fillMaxWidth().clickable(onClick = onClick),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
            Text(prettyCategory(state.category), style = MaterialTheme.typography.bodyLarge)
            Text(
                if (state.hasBudget) {
                    "₹${Engine.money(state.spentMinor)} / ₹${Engine.money(state.limitMinor)}"
                } else {
                    "₹${Engine.money(state.spentMinor)}  ·  set a budget"
                },
                style = MaterialTheme.typography.bodyMedium,
                fontWeight = FontWeight.Medium,
            )
        }

        if (state.hasBudget) {
            LinearProgressIndicator(
                progress = { fraction },
                modifier = Modifier.fillMaxWidth(),
                color = statusColour(state.status),
            )
            Text(statusLine(state), style = MaterialTheme.typography.bodySmall, color = statusColour(state.status))
        }
    }
}

private fun statusLine(state: EnvelopeState): String = when (state.status) {
    "over" -> "₹${Engine.money(state.spentMinor - state.limitMinor)} over"
    "warn" -> "₹${Engine.money(state.limitMinor - state.spentMinor)} left"
    "projected_over" -> "on pace for ₹${Engine.money(state.projectedMinor)} by month end"
    else -> "₹${Engine.money(state.limitMinor - state.spentMinor)} left"
}

@Composable
private fun statusColour(status: String): Color = when (status) {
    "over" -> MaterialTheme.colorScheme.error
    "warn" -> MaterialTheme.colorScheme.tertiary
    "projected_over" -> MaterialTheme.colorScheme.tertiary
    else -> MaterialTheme.colorScheme.primary
}

@Composable
fun TransactionRow(txn: Transaction) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(
                txn.displayName(),
                style = MaterialTheme.typography.bodyLarge,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            Text(
                prettyCategory(txn.category),
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        Text(
            (if (txn.isDebit) "−₹" else "+₹") + Engine.money(txn.amountMinor),
            style = MaterialTheme.typography.bodyLarge,
            color = if (txn.isDebit) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.primary,
        )
    }
}

/**
 * The in-app version of the review prompt.
 *
 * It shows the original message text, because the whole reason this transaction
 * needs a human is that the engine could not read it confidently, and the user
 * cannot judge the question without seeing what khata saw.
 */
@Composable
fun ReviewCard(txn: Transaction, categories: List<String>, onPick: (String) -> Unit) {
    Card {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                Text(txn.displayName(), style = MaterialTheme.typography.titleSmall)
                Text(
                    (if (txn.isDebit) "−₹" else "+₹") + Engine.money(txn.amountMinor),
                    style = MaterialTheme.typography.titleSmall,
                )
            }

            Text(
                txn.rawText,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 3,
                overflow = TextOverflow.Ellipsis,
            )

            Text(
                "Confidence ${(txn.confidence * 100).toInt()}%",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            LazyRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                items(categories, key = { it }) { category ->
                    AssistChip(onClick = { onPick(category) }, label = { Text(prettyCategory(category)) })
                }
            }
        }
    }
}

fun prettyCategory(category: String): String =
    category.replace('_', ' ').replaceFirstChar { it.uppercase() }
