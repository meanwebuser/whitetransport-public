package bypass.whitelist.ui

import androidx.fragment.app.Fragment

interface MainActivityHost {
    fun pushSubPage(fragment: Fragment)
    fun popSubPage()
}
