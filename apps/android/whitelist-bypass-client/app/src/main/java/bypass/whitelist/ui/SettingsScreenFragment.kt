package bypass.whitelist.ui

import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.view.isNotEmpty
import androidx.fragment.app.Fragment
import bypass.whitelist.App
import bypass.whitelist.R
import bypass.whitelist.tunnel.SplitTunnelingMode
import bypass.whitelist.tunnel.TunnelMode
import bypass.whitelist.util.Callback
import bypass.whitelist.util.ParamCallback
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.UiColors
import bypass.whitelist.util.LanguageMode
import bypass.whitelist.util.AccentMode
import bypass.whitelist.util.ThemeMode
import com.google.android.material.materialswitch.MaterialSwitch

class SettingsScreenFragment : Fragment(R.layout.fragment_settings_screen) {

    interface Host {
        fun onTunnelModeChanged(mode: TunnelMode)
        fun onForgetAllDestinations()
        fun onResetAllSettings()
        fun onCheckForUpdates()
        fun onCopySocksPressed()
    }

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        val root = view.findViewById<LinearLayout>(R.id.settingsContent)
        root.removeAllViews()
        root.addView(buildAppearanceSection())
        root.addView(buildTunnelSection())
        root.addView(buildNetworkSection())
        root.addView(buildBehaviorSection())
        root.addView(buildAboutSection())
        root.addView(buildDangerSection())
    }

    override fun onResume() {
        super.onResume()
        rebuild()
    }

    fun refresh() {
        rebuild()
    }

    private fun host(): Host? = activity as? Host

    private fun buildAppearanceSection(): View {
        val section = newSection(R.string.settings_section_appearance)
        val card = section.findViewById<LinearLayout>(R.id.sectionCard)
        addRow(card, R.drawable.ic_setting_theme, getString(R.string.settings_row_theme), getString(R.string.settings_row_theme_sub), themeLabel(Prefs.themeMode)) {
            ChoiceActionSheet.show(
                manager = parentFragmentManager,
                title = getString(R.string.settings_row_theme),
                subtitle = getString(R.string.settings_row_theme_sub),
                options = ThemeMode.entries.map { ChoiceActionSheet.Option(it.name, themeLabel(it)) },
                selectedId = Prefs.themeMode.name,
            ) { picked ->
                val mode = ThemeMode.valueOf(picked.id)
                if (mode != Prefs.themeMode) {
                    Prefs.themeMode = mode
                    App.applyTheme(mode)
                    rebuild()
                }
            }
        }
        addRow(card, R.drawable.ic_setting_theme, getString(R.string.settings_row_accent), getString(R.string.settings_row_accent_sub), accentLabel(Prefs.accentMode)) {
            ChoiceActionSheet.show(
                manager = parentFragmentManager,
                title = getString(R.string.settings_row_accent),
                subtitle = getString(R.string.settings_row_accent_sub),
                options = AccentMode.entries.map { ChoiceActionSheet.Option(it.name, accentLabel(it)) },
                selectedId = Prefs.accentMode.name,
            ) { picked ->
                val mode = AccentMode.valueOf(picked.id)
                if (mode != Prefs.accentMode) {
                    Prefs.accentMode = mode
                    activity?.recreate()
                }
            }
        }
        addRow(card, R.drawable.ic_nav_settings, getString(R.string.settings_row_language), getString(R.string.settings_row_language_sub), languageLabel(Prefs.languageMode)) {
            ChoiceActionSheet.show(
                manager = parentFragmentManager,
                title = getString(R.string.settings_row_language),
                subtitle = getString(R.string.settings_row_language_sub),
                options = LanguageMode.entries.map { ChoiceActionSheet.Option(it.name, languageLabel(it)) },
                selectedId = Prefs.languageMode.name,
            ) { picked ->
                val mode = LanguageMode.valueOf(picked.id)
                if (mode != Prefs.languageMode) {
                    Prefs.languageMode = mode
                    App.applyLanguage(mode)
                    activity?.recreate()
                }
            }
        }
        return section
    }

    private fun buildTunnelSection(): View {
        val section = newSection(R.string.settings_section_tunnel)
        val card = section.findViewById<LinearLayout>(R.id.sectionCard)

        addRow(card, R.drawable.ic_setting_tunnel, getString(R.string.settings_row_tunnel_mode), null, Prefs.tunnelMode.label) {
            ChoiceActionSheet.show(
                manager = parentFragmentManager,
                title = getString(R.string.settings_row_tunnel_mode),
                options = TunnelMode.entries.map { ChoiceActionSheet.Option(it.name, it.label) },
                selectedId = Prefs.tunnelMode.name,
            ) { picked ->
                val newMode = TunnelMode.valueOf(picked.id)
                if (newMode != Prefs.tunnelMode) {
                    Prefs.tunnelMode = newMode
                    host()?.onTunnelModeChanged(newMode)
                    rebuild()
                }
            }
        }

        val vp8SubRes = if (Prefs.dualTrack) R.string.settings_row_vp8_sub_dual else R.string.settings_row_vp8_sub
        addRow(card, R.drawable.ic_setting_vp8, getString(R.string.settings_row_vp8), getString(vp8SubRes, Prefs.vp8Fps, Prefs.vp8Batch), null) {
            Vp8ActionSheet.show(parentFragmentManager, Prefs.vp8Fps, Prefs.vp8Batch, Prefs.dualTrack) { fps, batch, dual ->
                Prefs.vp8Fps = fps
                Prefs.vp8Batch = batch
                Prefs.dualTrack = dual
                rebuild()
            }
        }

        addRow(card, R.drawable.ic_setting_autofill, getString(R.string.settings_row_autofill), if (Prefs.autofillEnabled) Prefs.autofillName else getString(R.string.settings_row_autofill_off), null) {
            AutofillActionSheet.show(parentFragmentManager) { rebuild() }
        }

        return section
    }

    private fun buildNetworkSection(): View {
        val section = newSection(R.string.settings_section_network)
        val card = section.findViewById<LinearLayout>(R.id.sectionCard)

        val splitSummary = if (Prefs.splitTunnelingMode == SplitTunnelingMode.NONE) {
            Prefs.splitTunnelingMode.label
        } else {
            resources.getQuantityString(R.plurals.split_tunneling_summary_count, Prefs.splitTunnelingPackages.size, Prefs.splitTunnelingMode.label, Prefs.splitTunnelingPackages.size)
        }
        addRow(card, R.drawable.ic_setting_split, getString(R.string.settings_row_split), splitSummary, null) {
            (activity as? MainActivityHost)?.pushSubPage(SplitTunnelingScreenFragment())
        }

        addRow(card, R.drawable.ic_setting_proxy, getString(R.string.settings_row_proxy), getString(R.string.settings_row_proxy_sub, Prefs.socksPort), null) {
            ProxyActionSheet.show(parentFragmentManager) { rebuild() }
        }

        addRow(card, R.drawable.ic_setting_proxy, getString(R.string.settings_row_copy_socks), getString(R.string.settings_row_copy_socks_sub), null) {
            host()?.onCopySocksPressed()
        }

        addRow(card, R.drawable.ic_setting_dns, getString(R.string.settings_row_dns), Prefs.dnsMode.label, null) {
            DnsActionSheet.show(parentFragmentManager) { rebuild() }
        }

        return section
    }

    private fun buildBehaviorSection(): View {
        val section = newSection(R.string.settings_section_behavior)
        val card = section.findViewById<LinearLayout>(R.id.sectionCard)

        addSwitchRow(card, R.drawable.ic_setting_headless, getString(R.string.settings_row_headless), getString(R.string.settings_row_headless_sub), Prefs.headless) { checked ->
            Prefs.headless = checked
        }
        addSwitchRow(card, R.drawable.ic_setting_reconnect, getString(R.string.settings_row_reconnect), getString(R.string.settings_row_reconnect_sub), Prefs.connectOnStart) { checked ->
            Prefs.connectOnStart = checked
        }
        addSwitchRow(card, R.drawable.ic_nav_activity, getString(R.string.settings_row_notification_status_text), getString(R.string.settings_row_notification_status_text_sub), Prefs.showNotificationStatusText) { checked ->
            Prefs.showNotificationStatusText = checked
        }
        addSwitchRow(card, R.drawable.ic_nav_activity, getString(R.string.settings_row_telemetry), getString(R.string.settings_row_telemetry_sub), Prefs.telemetryEnabled) { checked ->
            Prefs.telemetryEnabled = checked
        }
        return section
    }


    private fun buildAboutSection(): View {
        val section = newSection(R.string.settings_section_about)
        val card = section.findViewById<LinearLayout>(R.id.sectionCard)
        addStaticRow(
            card = card,
            iconRes = R.drawable.ic_nav_settings,
            title = getString(R.string.settings_row_app_version),
            sub = getString(R.string.settings_row_app_version_sub),
            trail = appVersionLabel(),
        )
        addRow(
            card,
            R.drawable.ic_nav_settings,
            getString(R.string.settings_row_check_updates),
            getString(R.string.settings_row_check_updates_sub),
            null,
        ) { host()?.onCheckForUpdates() }
        return section
    }

    private fun buildDangerSection(): View {
        val section = newSection(R.string.settings_section_danger)
        val card = section.findViewById<LinearLayout>(R.id.sectionCard)
        addRow(card, R.drawable.ic_setting_reset, getString(R.string.settings_reset_all), getString(R.string.settings_reset_all_sub), null, danger = true) {
            ConfirmActionSheet.show(
                manager = parentFragmentManager,
                title = getString(R.string.settings_reset_all),
                subtitle = getString(R.string.settings_reset_all_sub),
                confirmLabel = getString(R.string.confirm_reset),
                cancelLabel = getString(R.string.sheet_cancel),
                destructive = true,
            ) { host()?.onResetAllSettings() }
        }
        addRow(card, R.drawable.ic_setting_trash, getString(R.string.settings_forget_all_destinations), getString(R.string.settings_forget_all_destinations_sub), null, danger = true) {
            ConfirmActionSheet.show(
                manager = parentFragmentManager,
                title = getString(R.string.settings_forget_all_destinations),
                subtitle = getString(R.string.settings_forget_all_destinations_sub),
                confirmLabel = getString(R.string.confirm_forget),
                cancelLabel = getString(R.string.sheet_cancel),
                destructive = true,
            ) { host()?.onForgetAllDestinations() }
        }
        return section
    }

    private fun newSection(labelRes: Int): View {
        val parent = view as ViewGroup?
        val v = layoutInflater.inflate(R.layout.item_settings_section, parent, false)
        v.findViewById<TextView>(R.id.sectionLabel).setText(labelRes)
        v.findViewById<View>(R.id.sectionCard).clipToOutline = true
        return v
    }

    private fun addRow(
        card: LinearLayout,
        iconRes: Int,
        title: String,
        sub: String?,
        trail: String?,
        danger: Boolean = false,
        onClick: Callback,
    ) {
        val row = layoutInflater.inflate(R.layout.item_settings_row, card, false)
        row.findViewById<ImageView>(R.id.rowIcon).setImageResource(iconRes)
        row.findViewById<ImageView>(R.id.rowIcon).setColorFilter(UiColors.accent(requireContext()))
        if (danger) {
            row.findViewById<View>(R.id.rowIconBox).setBackgroundResource(R.drawable.bg_settings_row_icon_danger)
            row.findViewById<ImageView>(R.id.rowIcon).setColorFilter(requireContext().getColor(R.color.error_red))
            row.findViewById<TextView>(R.id.rowTitle).setTextColor(requireContext().getColor(R.color.error_red))
        }
        row.findViewById<TextView>(R.id.rowTitle).text = title
        row.findViewById<TextView>(R.id.rowSub).apply {
            if (sub.isNullOrBlank()) { visibility = View.GONE } else { text = sub; visibility = View.VISIBLE }
        }
        row.findViewById<TextView>(R.id.rowTrail).apply {
            if (trail.isNullOrBlank()) { visibility = View.GONE } else { text = trail; visibility = View.VISIBLE }
        }
        row.findViewById<ImageView>(R.id.rowChev).visibility = View.VISIBLE
        row.setOnClickListener { onClick() }
        if (card.isNotEmpty()) addDividerTo(card)
        card.addView(row)
    }


    private fun addStaticRow(
        card: LinearLayout,
        iconRes: Int,
        title: String,
        sub: String?,
        trail: String?,
    ) {
        val row = layoutInflater.inflate(R.layout.item_settings_row, card, false)
        row.findViewById<ImageView>(R.id.rowIcon).setImageResource(iconRes)
        row.findViewById<ImageView>(R.id.rowIcon).setColorFilter(UiColors.accent(requireContext()))
        row.findViewById<TextView>(R.id.rowTitle).text = title
        row.findViewById<TextView>(R.id.rowSub).apply {
            if (sub.isNullOrBlank()) { visibility = View.GONE } else { text = sub; visibility = View.VISIBLE }
        }
        row.findViewById<TextView>(R.id.rowTrail).apply {
            if (trail.isNullOrBlank()) { visibility = View.GONE } else { text = trail; visibility = View.VISIBLE }
        }
        row.findViewById<ImageView>(R.id.rowChev).visibility = View.GONE
        row.isClickable = false
        if (card.isNotEmpty()) addDividerTo(card)
        card.addView(row)
    }

    private fun addSwitchRow(
        card: LinearLayout,
        iconRes: Int,
        title: String,
        sub: String?,
        initial: Boolean,
        onToggled: ParamCallback<Boolean>,
    ) {
        val row = layoutInflater.inflate(R.layout.item_settings_row, card, false)
        row.findViewById<ImageView>(R.id.rowIcon).setImageResource(iconRes)
        row.findViewById<ImageView>(R.id.rowIcon).setColorFilter(UiColors.accent(requireContext()))
        row.findViewById<TextView>(R.id.rowTitle).text = title
        row.findViewById<TextView>(R.id.rowSub).apply {
            if (sub.isNullOrBlank()) { visibility = View.GONE } else { text = sub; visibility = View.VISIBLE }
        }
        val sw = row.findViewById<MaterialSwitch>(R.id.rowSwitch)
        sw.visibility = View.VISIBLE
        sw.isChecked = initial
        row.setOnClickListener {
            sw.isChecked = !sw.isChecked
            onToggled(sw.isChecked)
        }
        if (card.isNotEmpty()) addDividerTo(card)
        card.addView(row)
    }

    private fun addDividerTo(card: LinearLayout) {
        val divider = View(requireContext()).apply {
            layoutParams = LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, 1).apply {
                marginStart = (14 * resources.displayMetrics.density).toInt()
                marginEnd = (14 * resources.displayMetrics.density).toInt()
            }
            setBackgroundColor(requireContext().getColor(R.color.hair))
        }
        card.addView(divider)
    }


    private fun themeLabel(mode: ThemeMode): String = when (mode) {
        ThemeMode.SYSTEM -> getString(R.string.settings_language_system)
        ThemeMode.LIGHT -> if (resources.configuration.locales.get(0).language == "ru") "Светлая" else "Light"
        ThemeMode.DARK -> if (resources.configuration.locales.get(0).language == "ru") "Тёмная" else "Dark"
    }

    private fun accentLabel(mode: AccentMode): String = when (mode) {
        AccentMode.BLUE -> getString(R.string.settings_accent_blue)
        AccentMode.RED -> getString(R.string.settings_accent_red)
    }

    private fun languageLabel(mode: LanguageMode): String = when (mode) {
        LanguageMode.SYSTEM -> getString(R.string.settings_language_system)
        LanguageMode.RU -> getString(R.string.settings_language_ru)
    }


    @Suppress("DEPRECATION")
    private fun appVersionLabel(): String {
        return try {
            val info = requireContext().packageManager.getPackageInfo(requireContext().packageName, 0)
            val code = if (android.os.Build.VERSION.SDK_INT >= 28) info.longVersionCode else info.versionCode.toLong()
            "${info.versionName} ($code)"
        } catch (_: Exception) {
            "unknown"
        }
    }

    private fun rebuild() {
        if (!isAdded) return
        val rootView = view ?: return
        onViewCreated(rootView, null)
    }
}
