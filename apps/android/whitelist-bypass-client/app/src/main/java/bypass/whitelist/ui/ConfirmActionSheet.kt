package bypass.whitelist.ui

import android.content.res.ColorStateList
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.fragment.app.FragmentManager
import bypass.whitelist.R
import bypass.whitelist.util.UiColors
import bypass.whitelist.util.Callback
import com.google.android.material.bottomsheet.BottomSheetDialogFragment
import com.google.android.material.button.MaterialButton

class ConfirmActionSheet : BottomSheetDialogFragment() {

    private var titleText: String = ""
    private var subtitleText: String = ""
    private var confirmLabel: String = ""
    private var cancelLabel: String = ""
    private var destructive: Boolean = false
    private var onConfirm: Callback? = null

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.sheet_action_confirm, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        val titleView = view.findViewById<TextView>(R.id.sheetTitle)
        val subView = view.findViewById<TextView>(R.id.sheetSubtitle)
        val cancel = view.findViewById<MaterialButton>(R.id.buttonCancel)
        val confirm = view.findViewById<MaterialButton>(R.id.buttonConfirm)

        titleView.text = titleText
        subView.text = subtitleText
        cancel.text = cancelLabel
        confirm.text = confirmLabel

        if (destructive) {
            val context = requireContext()
            titleView.setTextColor(UiColors.accent(context))
            confirm.backgroundTintList = ColorStateList.valueOf(UiColors.accent(context))
            confirm.setTextColor(context.getColor(R.color.panel_bg))
        }

        cancel.setOnClickListener { dismiss() }
        confirm.setOnClickListener {
            onConfirm?.invoke()
            dismiss()
        }
    }

    companion object {
        fun show(
            manager: FragmentManager,
            title: String,
            subtitle: String,
            confirmLabel: String,
            cancelLabel: String = "Cancel",
            destructive: Boolean = false,
            onConfirm: Callback,
        ) {
            ConfirmActionSheet().apply {
                this.titleText = title
                this.subtitleText = subtitle
                this.confirmLabel = confirmLabel
                this.cancelLabel = cancelLabel
                this.destructive = destructive
                this.onConfirm = onConfirm
            }.show(manager, "ConfirmActionSheet")
        }
    }
}
