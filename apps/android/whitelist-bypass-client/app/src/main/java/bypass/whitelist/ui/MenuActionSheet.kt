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
import bypass.whitelist.util.UiColors
import bypass.whitelist.util.ParamCallback
import com.google.android.material.bottomsheet.BottomSheetDialogFragment

class MenuActionSheet : BottomSheetDialogFragment() {

    data class MenuItem(
        val id: String,
        val title: String,
        val iconRes: Int,
        val value: String? = null,
        val danger: Boolean = false,
    )

    private var titleText: String = ""
    private var subtitleText: String? = null
    private var items: List<MenuItem> = emptyList()
    private var onSelect: ParamCallback<MenuItem>? = null

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.sheet_action_menu, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        val titleView = view.findViewById<TextView>(R.id.sheetTitle)
        val subView = view.findViewById<TextView>(R.id.sheetSubtitle)
        val menuContainer = view.findViewById<LinearLayout>(R.id.menuContainer)

        titleView.text = titleText
        if (subtitleText.isNullOrBlank()) {
            subView.visibility = View.GONE
        } else {
            subView.text = subtitleText
            subView.visibility = View.VISIBLE
        }

        val inflater = LayoutInflater.from(requireContext())
        items.forEach { item ->
            val row = inflater.inflate(R.layout.item_action_menu_row, menuContainer, false)
            row.clipToOutline = true
            val label = row.findViewById<TextView>(R.id.menuLabel)
            val valueView = row.findViewById<TextView>(R.id.menuValue)
            val icon = row.findViewById<ImageView>(R.id.menuIcon)
            val iconBox = row.findViewById<View>(R.id.menuIconBox)

            label.text = item.title
            if (item.value.isNullOrBlank()) {
                valueView.visibility = View.GONE
            } else {
                valueView.text = item.value
                valueView.visibility = View.VISIBLE
            }
            icon.setImageResource(item.iconRes)
            if (item.danger) {
                label.setTextColor(UiColors.accent(requireContext()))
                iconBox.setBackgroundResource(R.drawable.bg_settings_row_icon_danger)
                icon.setColorFilter(UiColors.accent(requireContext()))
            }
            row.setOnClickListener {
                onSelect?.invoke(item)
                dismiss()
            }
            menuContainer.addView(row)
        }
    }

    companion object {
        fun show(
            manager: FragmentManager,
            title: String,
            subtitle: String? = null,
            items: List<MenuItem>,
            onSelect: ParamCallback<MenuItem>,
        ) {
            MenuActionSheet().apply {
                this.titleText = title
                this.subtitleText = subtitle
                this.items = items
                this.onSelect = onSelect
            }.show(manager, "MenuActionSheet")
        }
    }
}
