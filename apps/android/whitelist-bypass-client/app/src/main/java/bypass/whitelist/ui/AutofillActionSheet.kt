package bypass.whitelist.ui

import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import androidx.fragment.app.FragmentManager
import bypass.whitelist.R
import bypass.whitelist.util.Callback
import bypass.whitelist.util.Prefs
import com.google.android.material.bottomsheet.BottomSheetDialogFragment
import com.google.android.material.button.MaterialButton
import com.google.android.material.materialswitch.MaterialSwitch

class AutofillActionSheet : BottomSheetDialogFragment() {

    private var onSaved: Callback? = null

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.sheet_action_autofill, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        view.findViewById<View>(R.id.autofillNameCard).clipToOutline = true
        val switch = view.findViewById<MaterialSwitch>(R.id.autofillSwitch)
        val nameInput = view.findViewById<EditText>(R.id.autofillNameInput)
        val randomBtn = view.findViewById<MaterialButton>(R.id.autofillRandomButton)
        val cancelBtn = view.findViewById<MaterialButton>(R.id.autofillCancelButton)
        val saveBtn = view.findViewById<MaterialButton>(R.id.autofillSaveButton)

        switch.isChecked = Prefs.autofillEnabled
        nameInput.setText(Prefs.autofillName)

        randomBtn.setOnClickListener {
            val names = requireContext().assets.open("names.txt").bufferedReader().readLines()
            if (names.isNotEmpty()) nameInput.setText(names.random())
        }
        cancelBtn.setOnClickListener { dismiss() }
        saveBtn.setOnClickListener {
            Prefs.autofillEnabled = switch.isChecked
            Prefs.autofillName = nameInput.text.toString().trim()
            onSaved?.invoke()
            dismiss()
        }
    }

    companion object {
        fun show(manager: FragmentManager, onSaved: Callback) {
            AutofillActionSheet().apply {
                this.onSaved = onSaved
            }.show(manager, "AutofillActionSheet")
        }
    }
}
