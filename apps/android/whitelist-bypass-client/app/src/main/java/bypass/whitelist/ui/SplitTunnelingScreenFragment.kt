package bypass.whitelist.ui

import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.os.Bundle
import android.text.Editable
import android.text.TextWatcher
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.CheckBox
import android.widget.EditText
import android.widget.ImageButton
import android.widget.LinearLayout
import android.widget.ListView
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
import androidx.fragment.app.Fragment
import bypass.whitelist.R
import bypass.whitelist.tunnel.SplitTunnelingMode
import bypass.whitelist.tunnel.TunnelVpnService
import bypass.whitelist.util.Prefs
import java.lang.ref.WeakReference

class SplitTunnelingScreenFragment : Fragment() {

    private var mode: SplitTunnelingMode = Prefs.splitTunnelingMode
    private var packages: MutableSet<String> = Prefs.splitTunnelingPackages.toMutableSet()

    private lateinit var summary: TextView
    private lateinit var appsHeader: View
    private lateinit var appsArea: View
    private lateinit var appsListContainer: View
    private lateinit var loadingBar: ProgressBar
    private lateinit var searchInput: EditText
    private lateinit var systemAppsCheckbox: CheckBox
    private lateinit var appsList: ListView

    private lateinit var modeOffRow: View
    private lateinit var modeBypassRow: View
    private lateinit var modeOnlyRow: View
    private lateinit var modeOffCheck: View
    private lateinit var modeBypassCheck: View
    private lateinit var modeOnlyCheck: View

    private var allApps: List<SplitTunnelingAppItem> = emptyList()
    private var includeSystemApps: Boolean = false

    override fun onCreateView(
        inflater: LayoutInflater,
        container: ViewGroup?,
        savedInstanceState: Bundle?,
    ): View = inflater.inflate(R.layout.fragment_split_tunneling, container, false)

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        view.findViewById<View>(R.id.modeCard).clipToOutline = true
        summary = view.findViewById(R.id.splitSummary)
        appsHeader = view.findViewById(R.id.appsHeader)
        appsArea = view.findViewById(R.id.appsArea)
        appsListContainer = view.findViewById(R.id.appsListContainer)
        loadingBar = view.findViewById(R.id.loadingBar)
        searchInput = view.findViewById(R.id.searchInput)
        systemAppsCheckbox = view.findViewById(R.id.systemAppsCheckbox)
        appsList = view.findViewById(R.id.appsListView)
        modeOffRow = view.findViewById(R.id.modeOff)
        modeBypassRow = view.findViewById(R.id.modeBypass)
        modeOnlyRow = view.findViewById(R.id.modeOnly)
        modeOffCheck = view.findViewById(R.id.modeOffCheck)
        modeBypassCheck = view.findViewById(R.id.modeBypassCheck)
        modeOnlyCheck = view.findViewById(R.id.modeOnlyCheck)

        view.findViewById<ImageButton>(R.id.backButton).setOnClickListener { popSelf() }

        modeOffRow.setOnClickListener { applyMode(SplitTunnelingMode.NONE) }
        modeBypassRow.setOnClickListener { applyMode(SplitTunnelingMode.BYPASS) }
        modeOnlyRow.setOnClickListener { applyMode(SplitTunnelingMode.ONLY) }

        requireActivity().onBackPressedDispatcher.addCallback(viewLifecycleOwner, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                isEnabled = false
                popSelf()
            }
        })

        applyMode(mode, persist = false)
        refreshAppsList(initial = true)
    }

    private fun popSelf() {
        Prefs.splitTunnelingMode = mode
        Prefs.splitTunnelingPackages = packages
        if (TunnelVpnService.instance?.isRunning == true) {
            Toast.makeText(requireContext(), R.string.split_tunneling_mode_changed, Toast.LENGTH_SHORT).show()
        }
        (activity as? MainActivityHost)?.popSubPage()
    }

    private fun applyMode(newMode: SplitTunnelingMode, persist: Boolean = true) {
        mode = newMode
        if (persist) Prefs.splitTunnelingMode = newMode
        modeOffCheck.visibility = if (newMode == SplitTunnelingMode.NONE) View.VISIBLE else View.INVISIBLE
        modeBypassCheck.visibility = if (newMode == SplitTunnelingMode.BYPASS) View.VISIBLE else View.INVISIBLE
        modeOnlyCheck.visibility = if (newMode == SplitTunnelingMode.ONLY) View.VISIBLE else View.INVISIBLE
        val showApps = newMode != SplitTunnelingMode.NONE
        appsHeader.visibility = if (showApps) View.VISIBLE else View.GONE
        appsArea.visibility = if (showApps) View.VISIBLE else View.GONE
        updateSummary()
    }

    private fun updateSummary() {
        summary.text = if (mode == SplitTunnelingMode.NONE) {
            getString(R.string.split_tunneling_summary_off)
        } else {
            resources.getQuantityString(R.plurals.split_tunneling_summary_count, packages.size, mode.label, packages.size)
        }
    }

    private fun refreshAppsList(initial: Boolean) {
        if (!initial && allApps.isNotEmpty()) {
            applyFilters()
            return
        }
        loadingBar.visibility = View.VISIBLE
        appsListContainer.visibility = View.GONE
        val ctx = context ?: return
        val pm = ctx.packageManager
        val ownPackage = ctx.packageName
        val selectedSnapshot = packages.toSet()
        val act = activity ?: return
        val weakSelf = WeakReference(this)
        Thread {
            val loaded = pm.getInstalledApplications(PackageManager.GET_META_DATA)
                .filter { it.packageName != ownPackage }
                .mapNotNull { info ->
                    val pkg = info.packageName
                    if (pkg.isBlank()) return@mapNotNull null
                    val label = info.loadLabel(pm).toString().takeIf { it.isNotBlank() } ?: pkg
                    SplitTunnelingAppItem(
                        pkg, label, pm.getApplicationIcon(pkg),
                        selectedSnapshot.contains(pkg),
                        (info.flags and ApplicationInfo.FLAG_SYSTEM) == 0,
                    )
                }
                .distinctBy { it.packageName }
                .sortedWith(compareByDescending<SplitTunnelingAppItem> { it.isSelected }.thenBy { it.label.lowercase() })
            if (weakSelf.get() == null) return@Thread
            act.runOnUiThread {
                val self = weakSelf.get() ?: return@runOnUiThread
                if (!self.isAdded) return@runOnUiThread
                self.allApps = loaded
                self.loadingBar.visibility = View.GONE
                self.appsListContainer.visibility = View.VISIBLE
                val adapter = SplitTunnelingAdapter(self.layoutInflater, self.packages)
                self.appsList.adapter = adapter
                self.applyFilters()
                self.systemAppsCheckbox.isChecked = self.includeSystemApps
                self.systemAppsCheckbox.setOnCheckedChangeListener { _, checked ->
                    val s = weakSelf.get() ?: return@setOnCheckedChangeListener
                    s.includeSystemApps = checked
                    s.applyFilters()
                }
                self.searchInput.addTextChangedListener(object : TextWatcher {
                    override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
                    override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {
                        weakSelf.get()?.applyFilters()
                    }
                    override fun afterTextChanged(s: Editable?) {
                        weakSelf.get()?.updateSummary()
                    }
                })
            }
        }.start()
    }

    private fun applyFilters() {
        val adapter = appsList.adapter as? SplitTunnelingAdapter ?: return
        val query = searchInput.text.toString()
        val base = allApps.filter { includeSystemApps || it.isUserApp }
        val filtered = if (query.isBlank()) base else base.filter {
            it.label.contains(query, ignoreCase = true) || it.packageName.contains(query, ignoreCase = true)
        }
        adapter.items = filtered
        updateSummary()
    }
}
