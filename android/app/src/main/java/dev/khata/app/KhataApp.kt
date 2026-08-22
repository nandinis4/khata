package dev.khata.app

import android.app.Application
import dev.khata.app.notify.Notifier

class KhataApp : Application() {
    override fun onCreate() {
        super.onCreate()
        // Channels must exist before the first notification is posted, and the
        // first one may come from the capture service before any screen opens.
        Notifier.ensureChannels(this)
    }
}
