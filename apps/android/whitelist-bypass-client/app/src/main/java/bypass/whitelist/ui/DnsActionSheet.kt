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
import bypass.whitelist.util.DnsMode
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.UiColors
import com.google.android.material.bottomsheet.BottomSheetDialogFragment
import com.google.android.material.button.MaterialButton

class DnsActionSheet : BottomSheetDialogFragment() {

    private var onSaved: Callback? = null
    private var selectedMode: DnsMode = DnsMode.SYSTEM

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.sheet_action_dns, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        selectedMode = Prefs.dnsMode
        val modeContainer = view.findViewById<LinearLayout>(R.id.dnsModeContainer)
        val customFields = view.findViewById<LinearLayout>(R.id.dnsCustomFields)
        val primary = view.findViewById<EditText>(R.id.dnsPrimaryInput)
        val secondary = view.findViewById<EditText>(R.id.dnsSecondaryInput)

        primary.setText(Prefs.dnsPrimary)
        secondary.setText(Prefs.dnsSecondary)
        customFields.visibility = if (selectedMode == DnsMode.CUSTOM) View.VISIBLE else View.GONE

        val inflater = LayoutInflater.from(requireContext())
        val rowViews = mutableMapOf<DnsMode, View>()
        DnsMode.entries.forEach { mode ->
            val row = inflater.inflate(R.layout.item_action_option, modeContainer, false)
            row.clipToOutline = true
            row.findViewById<TextView>(R.id.actionOptionTitle).text = mode.label
            row.findViewById<TextView>(R.id.actionOptionSub).visibility = View.GONE
            row.setOnClickListener {
                selectedMode = mode
                updateModeSelection(rowViews)
                customFields.visibility = if (mode == DnsMode.CUSTOM) View.VISIBLE else View.GONE
            }
            modeContainer.addView(row)
            rowViews[mode] = row
        }
        updateModeSelection(rowViews)

        view.findViewById<MaterialButton>(R.id.dnsCancelButton).setOnClickListener { dismiss() }
        view.findViewById<MaterialButton>(R.id.dnsSaveButton).setOnClickListener {
            Prefs.dnsMode = selectedMode
            if (selectedMode == DnsMode.CUSTOM) {
                Prefs.dnsPrimary = primary.text.toString().trim()
                Prefs.dnsSecondary = secondary.text.toString().trim()
            }
            onSaved?.invoke()
            dismiss()
        }
    }

    private fun updateModeSelection(rowViews: Map<DnsMode, View>) {
        val context = requireContext()
        rowViews.forEach { (mode, row) ->
            val isActive = mode == selectedMode
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
            DnsActionSheet().apply { this.onSaved = onSaved }.show(manager, "DnsActionSheet")
        }
    }
}
