package bypass.whitelist.ui

import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.fragment.app.FragmentManager
import bypass.whitelist.R
import bypass.whitelist.util.Callback
import bypass.whitelist.util.Net
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.UiColors
import bypass.whitelist.util.SocksAuthMode
import com.google.android.material.bottomsheet.BottomSheetDialogFragment
import com.google.android.material.button.MaterialButton
import com.google.android.material.materialswitch.MaterialSwitch

class ProxyActionSheet : BottomSheetDialogFragment() {

    private var onSaved: Callback? = null
    private var selectedAuth: SocksAuthMode = SocksAuthMode.AUTO

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.sheet_action_proxy, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        selectedAuth = Prefs.socksAuthMode
        view.findViewById<View>(R.id.proxyPortCard).clipToOutline = true
        val portInput = view.findViewById<EditText>(R.id.proxyPortInput)
        val hostInput = view.findViewById<EditText>(R.id.proxyHostInput)
        val authContainer = view.findViewById<LinearLayout>(R.id.proxyAuthContainer)
        val manualContainer = view.findViewById<LinearLayout>(R.id.proxyManualContainer)
        val userInput = view.findViewById<EditText>(R.id.proxyUserInput)
        val passInput = view.findViewById<EditText>(R.id.proxyPassInput)
        val proxyOnly = view.findViewById<MaterialSwitch>(R.id.proxyOnlySwitch)

        portInput.setText(Prefs.socksPort.toString())
        hostInput.setText(Prefs.socksHost)
        userInput.setText(Prefs.socksUser)
        passInput.setText(Prefs.socksPass)
        proxyOnly.isChecked = Prefs.proxyOnly
        manualContainer.visibility = if (selectedAuth == SocksAuthMode.MANUAL) View.VISIBLE else View.GONE

        val inflater = LayoutInflater.from(requireContext())
        val rowViews = mutableMapOf<SocksAuthMode, View>()
        val context = requireContext()
        SocksAuthMode.entries.forEach { mode ->
            val row = inflater.inflate(R.layout.item_action_option, authContainer, false)
            row.clipToOutline = true
            val title = when (mode) {
                SocksAuthMode.AUTO -> context.getString(R.string.proxy_auth_auto)
                SocksAuthMode.MANUAL -> context.getString(R.string.proxy_auth_manual)
            }
            val sub = when (mode) {
                SocksAuthMode.AUTO -> context.getString(R.string.proxy_auth_auto_sub)
                SocksAuthMode.MANUAL -> context.getString(R.string.proxy_auth_manual_sub)
            }
            row.findViewById<TextView>(R.id.actionOptionTitle).text = title
            row.findViewById<TextView>(R.id.actionOptionSub).apply {
                text = sub
                visibility = View.VISIBLE
            }
            row.setOnClickListener {
                selectedAuth = mode
                updateAuthSelection(rowViews)
                manualContainer.visibility = if (mode == SocksAuthMode.MANUAL) View.VISIBLE else View.GONE
            }
            authContainer.addView(row)
            rowViews[mode] = row
        }
        updateAuthSelection(rowViews)

        view.findViewById<MaterialButton>(R.id.proxyCancelButton).setOnClickListener { dismiss() }
        view.findViewById<MaterialButton>(R.id.proxySaveButton).setOnClickListener {
            portInput.text.toString().toLongOrNull()?.takeIf { it in 1L..65535L }?.let { Prefs.socksPort = it }
            Prefs.socksHost = hostInput.text.toString().trim().ifEmpty { Net.LOCALHOST }
            Prefs.socksAuthMode = selectedAuth
            if (selectedAuth == SocksAuthMode.MANUAL) {
                Prefs.socksUser = userInput.text.toString().trim()
                Prefs.socksPass = passInput.text.toString()
            }
            Prefs.proxyOnly = proxyOnly.isChecked
            onSaved?.invoke()
            dismiss()
        }
    }

    private fun updateAuthSelection(rowViews: Map<SocksAuthMode, View>) {
        val context = requireContext()
        rowViews.forEach { (mode, row) ->
            val isActive = mode == selectedAuth
            val titleView = row.findViewById<TextView>(R.id.actionOptionTitle)
            val checkBox = row.findViewById<View>(R.id.actionOptionCheckBox)
            val checkIcon = row.findViewById<ImageView>(R.id.actionOptionCheckIcon)
            if (isActive) {
                row.setBackgroundResource(R.drawable.bg_destination_card_active)
                checkBox.setBackgroundResource(R.drawable.bg_action_check_active)
                checkIcon.visibility = View.VISIBLE
                titleView.setTextColor(UiColors.accent(context))
            } else {
                row.setBackgroundResource(R.drawable.bg_destination_card)
                checkBox.setBackgroundResource(R.drawable.bg_action_check_idle)
                checkIcon.visibility = View.GONE
                titleView.setTextColor(context.getColor(R.color.ink))
            }
        }
    }

    companion object {
        fun show(manager: FragmentManager, onSaved: Callback) {
            ProxyActionSheet().apply { this.onSaved = onSaved }.show(manager, "ProxyActionSheet")
        }
    }
}
