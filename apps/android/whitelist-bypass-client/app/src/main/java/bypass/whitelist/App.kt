package bypass.whitelist

import android.app.Application
import androidx.appcompat.app.AppCompatDelegate
import androidx.core.os.LocaleListCompat
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.LanguageMode
import bypass.whitelist.util.ThemeMode

class App : Application() {
    override fun onCreate() {
        super.onCreate()
        Prefs.init(this)
        applyTheme(Prefs.themeMode)
        applyLanguage(Prefs.languageMode)
    }

    companion object {
        fun applyLanguage(mode: LanguageMode) {
            val locales = when (mode) {
                LanguageMode.SYSTEM -> LocaleListCompat.getEmptyLocaleList()
                LanguageMode.RU -> LocaleListCompat.forLanguageTags("ru")
            }
            AppCompatDelegate.setApplicationLocales(locales)
        }

        fun applyTheme(mode: ThemeMode) {
            val target = when (mode) {
                ThemeMode.SYSTEM -> AppCompatDelegate.MODE_NIGHT_FOLLOW_SYSTEM
                ThemeMode.LIGHT -> AppCompatDelegate.MODE_NIGHT_NO
                ThemeMode.DARK -> AppCompatDelegate.MODE_NIGHT_YES
            }
            AppCompatDelegate.setDefaultNightMode(target)
        }
    }
}
