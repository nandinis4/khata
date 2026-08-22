package dev.khata.app.notify

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.widget.Toast
import dev.khata.app.Engine

/**
 * Handles a category button on a review notification.
 *
 * This is the whole point of the notification: the user answers from the shade,
 * the transaction is filed, and the answer is recorded as a correction that the
 * toolchain later compiles into a rule. The same merchant will not ask again.
 */
class CategoryActionReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context?, intent: Intent?) {
        val ctx = context ?: return
        val txnId = intent?.getStringExtra(Notifier.EXTRA_TXN_ID) ?: return
        val category = intent.getStringExtra(Notifier.EXTRA_CATEGORY) ?: return
        val notificationId = intent.getIntExtra(Notifier.EXTRA_NOTIFICATION_ID, -1)

        // Dismiss first. If the write below is slow the user should not be left
        // tapping a button that appears to have done nothing.
        if (notificationId != -1) Notifier.dismiss(ctx, notificationId)

        val pending = goAsync()
        Thread {
            var ok = false
            try {
                ok = Engine.correct(ctx, txnId, category)
                if (!ok) Log.w(TAG, "failed to file $txnId as $category")
            } catch (t: Throwable) {
                Log.e(TAG, "category action failed", t)
            } finally {
                // Report what actually happened, and do it before finish():
                // once the pending result is finished the process is killable
                // and a toast still needs it alive to draw.
                val message = if (ok) {
                    "Filed as ${category.replace('_', ' ')}"
                } else {
                    "Could not file that — open khata to retry"
                }
                Handler(Looper.getMainLooper()).post {
                    Toast.makeText(ctx, message, Toast.LENGTH_SHORT).show()
                }
                pending.finish()
            }
        }.start()
    }

    private companion object {
        const val TAG = "khata.action"
    }
}
