package bypass.whitelist.ui

import android.graphics.Typeface
import android.os.Bundle
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import androidx.fragment.app.Fragment
import bypass.whitelist.R

class LogsFragment : Fragment(R.layout.fragment_logs_screen) {

    interface Host {
        fun activityLogLines(): List<String>
        fun copyLogs()
        fun shareLogs()
    }

    private enum class Category { ALL, LINK, OTHER }
    private enum class Level { INFO, DEBUG }

    private var container: LinearLayout? = null
    private var scrollView: ScrollView? = null
    private var category: Category = Category.ALL
    private var minLevel: Level = Level.INFO
    private var allLines: List<String> = emptyList()

    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        container = view.findViewById(R.id.eventsContainer)
        scrollView = view.findViewById(R.id.activityScroll)
        bindFilters(view)
        refresh(host = host())
        view.findViewById<Button>(R.id.buttonCopyRaw).setOnClickListener { host()?.copyLogs() }
        view.findViewById<Button>(R.id.buttonShareFile).setOnClickListener { host()?.shareLogs() }
    }

    override fun onDestroyView() {
        container = null
        scrollView = null
        super.onDestroyView()
    }

    fun refresh(host: Host?) {
        allLines = host?.activityLogLines().orEmpty()
        renderLines()
    }

    fun onLineAppended(line: String) {
        allLines = allLines + line
        if (matches(line)) {
            addLine(line)
            scrollToBottom()
        }
    }

    private fun bindFilters(view: View) {
        view.findViewById<Button>(R.id.logFilterAll).setOnClickListener { category = Category.ALL; renderLines() }
        view.findViewById<Button>(R.id.logFilterLink).setOnClickListener { category = Category.LINK; renderLines() }
        view.findViewById<Button>(R.id.logFilterOther).setOnClickListener { category = Category.OTHER; renderLines() }
        view.findViewById<Button>(R.id.logLevelInfo).setOnClickListener { minLevel = Level.INFO; renderLines() }
        view.findViewById<Button>(R.id.logLevelDebug).setOnClickListener { minLevel = Level.DEBUG; renderLines() }
    }

    private fun renderLines() {
        container?.removeAllViews()
        allLines.filter { matches(it) }.takeLast(350).forEach { addLine(it) }
        scrollToBottom()
    }

    private fun matches(line: String): Boolean {
        val parsed = parse(line)
        val categoryOk = when (category) {
            Category.ALL -> true
            Category.LINK -> parsed.category == "LINK"
            Category.OTHER -> parsed.category != "LINK"
        }
        val levelOk = minLevel == Level.DEBUG || parsed.level != "DEBUG"
        return categoryOk && levelOk
    }

    private data class ParsedLine(val level: String, val category: String, val direction: String, val text: String)

    private fun parse(line: String): ParsedLine {
        val regex = Regex("^\\[(DEBUG|INFO)\\]\\[(LINK|APP|OTHER)\\]\\s*([→←•])?\\s*(.*)$")
        val m = regex.find(line)
        if (m != null) {
            return ParsedLine(m.groupValues[1], m.groupValues[2], m.groupValues[3].ifBlank { "•" }, m.groupValues[4])
        }
        val lower = line.lowercase()
        val cat = if (lower.contains("relay") || lower.contains("tunnel") || lower.contains("vpn") || lower.contains("room") || lower.contains("connect") || lower.contains("telegram")) "LINK" else "APP"
        return ParsedLine("INFO", cat, "•", line)
    }

    private fun addLine(line: String) {
        val parsed = parse(line)
        val ctx = requireContext()
        val row = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(12), dp(10), dp(12), dp(10))
            setBackgroundResource(R.drawable.bg_ping_result_ok)
        }
        val header = TextView(ctx).apply {
            text = "${parsed.direction} ${parsed.category} · ${parsed.level}"
            textSize = 10f
            typeface = Typeface.MONOSPACE
            setTextColor(ctx.getColor(if (parsed.category == "LINK") R.color.accent_emerald else R.color.ink_3))
        }
        val body = TextView(ctx).apply {
            text = parsed.text
            textSize = 13f
            typeface = Typeface.MONOSPACE
            setTextColor(ctx.getColor(R.color.ink))
            setLineSpacing(0f, 1.08f)
        }
        row.addView(header)
        row.addView(body)
        val lp = LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT).apply {
            setMargins(dp(20), dp(6), dp(20), dp(6))
        }
        container?.addView(row, lp)
    }

    private fun scrollToBottom() {
        scrollView?.post { scrollView?.fullScroll(View.FOCUS_DOWN) }
    }

    private fun dp(value: Int): Int = (value * resources.displayMetrics.density).toInt()

    private fun host(): Host? = activity as? Host
}
