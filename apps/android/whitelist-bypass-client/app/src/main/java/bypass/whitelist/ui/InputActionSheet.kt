package bypass.whitelist.ui

import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.TextView
import androidx.fragment.app.FragmentManager
import bypass.whitelist.R
import bypass.whitelist.util.ParamCallback
import com.google.android.material.bottomsheet.BottomSheetDialogFragment
import com.google.android.material.button.MaterialButton

class InputActionSheet : BottomSheetDialogFragment() {

    private var titleText: String = ""
    private var subtitleText: String? = null
    private var fieldLabel: String = ""
    private var initialValue: String = ""
    private var onSave: ParamCallback<String>? = null

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.sheet_action_input, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        val titleView = view.findViewById<TextView>(R.id.sheetTitle)
        val subView = view.findViewById<TextView>(R.id.sheetSubtitle)
        val labelView = view.findViewById<TextView>(R.id.inputFieldLabel)
        val input = view.findViewById<EditText>(R.id.inputField)
        val cancel = view.findViewById<MaterialButton>(R.id.buttonCancel)
        val save = view.findViewById<MaterialButton>(R.id.buttonSave)

        titleView.text = titleText
        if (subtitleText.isNullOrBlank()) {
            subView.visibility = View.GONE
        } else {
            subView.text = subtitleText
            subView.visibility = View.VISIBLE
        }
        labelView.text = fieldLabel
        input.setText(initialValue)
        input.setSelection(input.text.length)

        cancel.setOnClickListener { dismiss() }
        save.setOnClickListener {
            val value = input.text.toString().trim()
            if (value.isNotEmpty()) {
                onSave?.invoke(value)
                dismiss()
            } else {
                input.requestFocus()
            }
        }
    }

    companion object {
        fun show(
            manager: FragmentManager,
            title: String,
            subtitle: String? = null,
            fieldLabel: String,
            initialValue: String,
            onSave: ParamCallback<String>,
        ) {
            InputActionSheet().apply {
                this.titleText = title
                this.subtitleText = subtitle
                this.fieldLabel = fieldLabel
                this.initialValue = initialValue
                this.onSave = onSave
            }.show(manager, "InputActionSheet")
        }
    }
}
