package bypass.whitelist.ui

import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.fragment.app.FragmentManager
import bypass.whitelist.R
import bypass.whitelist.util.ParamCallback
import bypass.whitelist.util.UiColors
import com.google.android.material.bottomsheet.BottomSheetDialogFragment

class ChoiceActionSheet : BottomSheetDialogFragment() {

    data class Option(val id: String, val title: String, val sub: String? = null)

    private var titleText: String = ""
    private var subtitleText: String? = null
    private var options: List<Option> = emptyList()
    private var selectedId: String? = null
    private var onSelect: ParamCallback<Option>? = null

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.sheet_action_choice, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        val titleView = view.findViewById<TextView>(R.id.sheetTitle)
        val subtitleView = view.findViewById<TextView>(R.id.sheetSubtitle)
        val optionsContainer = view.findViewById<LinearLayout>(R.id.optionsContainer)

        titleView.text = titleText
        if (subtitleText.isNullOrBlank()) {
            subtitleView.visibility = View.GONE
        } else {
            subtitleView.text = subtitleText
            subtitleView.visibility = View.VISIBLE
        }

        val inflater = LayoutInflater.from(requireContext())
        options.forEach { option ->
            val row = inflater.inflate(R.layout.item_action_option, optionsContainer, false)
            row.clipToOutline = true
            val rowTitle = row.findViewById<TextView>(R.id.actionOptionTitle)
            val rowSub = row.findViewById<TextView>(R.id.actionOptionSub)
            val checkBox = row.findViewById<View>(R.id.actionOptionCheckBox)
            val checkIcon = row.findViewById<ImageView>(R.id.actionOptionCheckIcon)

            rowTitle.text = option.title
            if (option.sub.isNullOrBlank()) {
                rowSub.visibility = View.GONE
            } else {
                rowSub.text = option.sub
                rowSub.visibility = View.VISIBLE
            }

            val isSelected = option.id == selectedId
            if (isSelected) {
                row.setBackgroundResource(R.drawable.bg_destination_card_active)
                checkBox.setBackgroundResource(R.drawable.bg_action_check_active)
                checkIcon.visibility = View.VISIBLE
                rowTitle.setTextColor(UiColors.accent(requireContext()))
            }

            row.setOnClickListener {
                onSelect?.invoke(option)
                dismiss()
            }
            optionsContainer.addView(row)
        }
    }

    companion object {
        fun show(
            manager: FragmentManager,
            title: String,
            subtitle: String? = null,
            options: List<Option>,
            selectedId: String?,
            onSelect: ParamCallback<Option>,
        ) {
            ChoiceActionSheet().apply {
                this.titleText = title
                this.subtitleText = subtitle
                this.options = options
                this.selectedId = selectedId
                this.onSelect = onSelect
            }.show(manager, "ChoiceActionSheet")
        }
    }
}
