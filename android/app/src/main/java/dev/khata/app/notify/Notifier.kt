package dev.khata.app.notify

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import dev.khata.app.BudgetAlert
import dev.khata.app.Engine
import dev.khata.app.R
import dev.khata.app.Transaction

/**
 * Notifications, used sparingly.
 *
 * The interaction this app is built around is: a transaction arrives, and if
 * khata cannot confidently file it, it asks — once — with the answers as
 * buttons. Tapping one files the transaction and teaches the engine, without
 * opening the app. Everything else the user would have to remember to do later,
 * and they will not.
 *
 * The restraint matters as much as the feature. A budgeting app that posts on
 * every transaction gets muted within a week, and a muted app captures nothing.
 * So: review prompts only when confidence is genuinely low, and budget alerts
 * only on a threshold crossing that has not fired before this month.
 */
object Notifier {

    private const val CHANNEL_REVIEW = "review"
    private const val CHANNEL_BUDGET = "budget"

    const val EXTRA_TXN_ID = "dev.khata.app.TXN_ID"
    const val EXTRA_CATEGORY = "dev.khata.app.CATEGORY"
    const val EXTRA_NOTIFICATION_ID = "dev.khata.app.NOTIFICATION_ID"

    /** Offered as buttons on a review prompt. Three is what fits before Android collapses them. */
    private val QUICK_CATEGORIES = listOf("groceries", "dining", "transport")

    fun ensureChannels(context: Context) {
        val manager = context.getSystemService(NotificationManager::class.java) ?: return

        manager.createNotificationChannel(
            NotificationChannel(
                CHANNEL_REVIEW,
                context.getString(R.string.channel_review),
                // DEFAULT, not HIGH: this is a question, not an emergency. It
                // should appear in the shade without taking over the screen.
                NotificationManager.IMPORTANCE_DEFAULT,
            ).apply {
                description = context.getString(R.string.channel_review_description)
                setShowBadge(true)
            }
        )

        manager.createNotificationChannel(
            NotificationChannel(
                CHANNEL_BUDGET,
                context.getString(R.string.channel_budget),
                NotificationManager.IMPORTANCE_DEFAULT,
            ).apply {
                description = context.getString(R.string.channel_budget_description)
            }
        )
    }

    /**
     * Asks what a transaction was, with the likely answers as buttons.
     *
     * The notification id is derived from the transaction id so that the same
     * transaction can never queue two prompts, and so the correct one can be
     * dismissed when the user answers.
     */
    fun askForCategory(context: Context, txn: Transaction) {
        if (!canPost(context)) return
        ensureChannels(context)

        val id = notificationIdFor(txn.id)
        val amount = "₹" + Engine.money(txn.amountMinor)
        val direction = if (txn.isDebit) "paid to" else "received from"

        val builder = NotificationCompat.Builder(context, CHANNEL_REVIEW)
            .setSmallIcon(R.drawable.ic_stat_khata)
            .setContentTitle("$amount $direction ${txn.displayName()}")
            .setContentText(context.getString(R.string.review_prompt))
            .setStyle(NotificationCompat.BigTextStyle().bigText(txn.rawText.take(240)))
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .setCategory(NotificationCompat.CATEGORY_STATUS)
            .setAutoCancel(true)
            .setOnlyAlertOnce(true)

        QUICK_CATEGORIES.forEach { category ->
            builder.addAction(
                NotificationCompat.Action.Builder(0, label(category), categoryIntent(context, txn.id, category, id))
                    .build()
            )
        }

        NotificationManagerCompat.from(context).notify(id, builder.build())
    }

    fun budgetAlert(context: Context, alert: BudgetAlert) {
        if (!canPost(context)) return
        ensureChannels(context)

        val notification = NotificationCompat.Builder(context, CHANNEL_BUDGET)
            .setSmallIcon(R.drawable.ic_stat_khata)
            .setContentTitle(alert.title)
            .setContentText(alert.body)
            .setStyle(NotificationCompat.BigTextStyle().bigText(alert.body))
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .setAutoCancel(true)
            .build()

        // One id per category and threshold, so a later crossing replaces
        // nothing and an identical one cannot stack.
        val id = notificationIdFor("budget:${alert.category}:${alert.threshold}")
        NotificationManagerCompat.from(context).notify(id, notification)
    }

    fun dismiss(context: Context, notificationId: Int) {
        NotificationManagerCompat.from(context).cancel(notificationId)
    }

    private fun categoryIntent(context: Context, txnId: String, category: String, notificationId: Int): PendingIntent {
        val intent = Intent(context, CategoryActionReceiver::class.java).apply {
            putExtra(EXTRA_TXN_ID, txnId)
            putExtra(EXTRA_CATEGORY, category)
            putExtra(EXTRA_NOTIFICATION_ID, notificationId)
            // Distinct data keeps Android from collapsing the three buttons
            // into one PendingIntent, which would file everything as groceries.
            data = android.net.Uri.parse("khata://categorise/$txnId/$category")
        }
        return PendingIntent.getBroadcast(
            context,
            0,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    private fun canPost(context: Context): Boolean =
        android.os.Build.VERSION.SDK_INT < android.os.Build.VERSION_CODES.TIRAMISU ||
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED

    private fun label(category: String): String =
        category.replace('_', ' ').replaceFirstChar { it.uppercase() }

    private fun notificationIdFor(key: String): Int = key.hashCode() and 0x7fffffff
}
