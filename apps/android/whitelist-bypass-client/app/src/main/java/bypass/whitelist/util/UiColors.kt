package bypass.whitelist.util

import android.content.Context
import android.content.res.ColorStateList
import androidx.annotation.ColorInt
import androidx.core.content.ContextCompat
import bypass.whitelist.R

object UiColors {
    @ColorInt
    fun accent(context: Context): Int = ContextCompat.getColor(
        context,
        when (Prefs.accentMode) {
            AccentMode.BLUE -> R.color.accent_emerald
            AccentMode.RED -> R.color.accent_red
        }
    )

    @ColorInt
    fun accentSoft(context: Context): Int = ContextCompat.getColor(
        context,
        when (Prefs.accentMode) {
            AccentMode.BLUE -> R.color.accent_emerald_soft
            AccentMode.RED -> R.color.accent_red_soft
        }
    )

    fun accentTint(context: Context): ColorStateList = ColorStateList.valueOf(accent(context))
}
