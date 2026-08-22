package dev.khata.app.ui

import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext

// A restrained ink-and-paper palette: this is a ledger, and the numbers should
// be the loudest thing on the screen.
private val Ink = Color(0xFF1B4332)
private val InkLight = Color(0xFF95D5B2)
private val Amber = Color(0xFFB07D14)
private val AmberLight = Color(0xFFE9C46A)

private val LightColors = lightColorScheme(
    primary = Ink,
    tertiary = Amber,
)

private val DarkColors = darkColorScheme(
    primary = InkLight,
    tertiary = AmberLight,
)

@Composable
fun KhataTheme(darkTheme: Boolean = isSystemInDarkTheme(), content: @Composable () -> Unit) {
    val context = LocalContext.current
    val colors = when {
        // Material You where available: a finance app people check daily should
        // look like it belongs on their phone rather than announcing itself.
        Build.VERSION.SDK_INT >= Build.VERSION_CODES.S ->
            if (darkTheme) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
        darkTheme -> DarkColors
        else -> LightColors
    }
    MaterialTheme(colorScheme = colors, content = content)
}
