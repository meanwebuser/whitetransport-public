package bypass.whitelist.ui

import android.animation.ValueAnimator
import android.view.LayoutInflater
import android.view.MotionEvent
import android.view.View
import android.view.HapticFeedbackConstants
import android.view.animation.AlphaAnimation
import android.view.animation.OvershootInterpolator
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import bypass.whitelist.R
import bypass.whitelist.tunnel.CallConfig
import bypass.whitelist.tunnel.VpnStatus
import bypass.whitelist.util.Callback
import bypass.whitelist.util.ParamCallback
import bypass.whitelist.util.Prefs
import bypass.whitelist.util.UiColors

class MainFragmentView(private val root: View) {

    private val headerSub: TextView = root.findViewById(R.id.headerSub)
    private val addButton: View = root.findViewById(R.id.headerAddButton)
    private val hero: View = root.findViewById(R.id.heroButton)
    private val heroLabel: TextView = root.findViewById(R.id.heroLabel)
    private val heroPowerIcon: ImageView = root.findViewById(R.id.heroPowerIcon)
    private val heroRingOuter: HeroRingOuterView = root.findViewById(R.id.heroRingOuter)
    private val heroRingMid: View = root.findViewById(R.id.heroRingMid)
    private val heroPulse: View = root.findViewById(R.id.heroPulse)
    private val statusHeadline: TextView = root.findViewById(R.id.statusHeadline)
    private val statusDot: View = root.findViewById(R.id.statusDot)
    private val statusDetail: TextView = root.findViewById(R.id.statusDetail)
    private val callsList: LinearLayout = root.findViewById(R.id.callsList)
    private val copySocksButton: View = root.findViewById(R.id.copySocksButton)
    private val copySocksButtonLabel: TextView = root.findViewById(R.id.copySocksButtonLabel)
    private val roomWarmupStatus: TextView = root.findViewById(R.id.roomWarmupStatus)
    private val roomRefreshButton: View = root.findViewById(R.id.roomRefreshButton)
    private val roomServerChoices: LinearLayout = root.findViewById(R.id.roomServerChoices)
    private val speedTestButton: View = root.findViewById(R.id.speedTestButton)
    private val speedTestButtonLabel: TextView = root.findViewById(R.id.speedTestButtonLabel)
    private val emptyCta: View = root.findViewById(R.id.emptyCta)
    private val statsCard: View = root.findViewById(R.id.statsCard)
    private val pingRow: LinearLayout = root.findViewById(R.id.pingRow)
    private val pingButton: View = root.findViewById(R.id.pingButton)
    private val pingButtonLabel: TextView = root.findViewById(R.id.pingButtonLabel)
    private val pingResult: View = root.findViewById(R.id.pingResult)
    private val pingResultHost: TextView = root.findViewById(R.id.pingResultHost)
    private val pingResultRtt: TextView = root.findViewById(R.id.pingResultRtt)
    private val statUptime: TextView = root.findViewById(R.id.statUptime)
    private val statMode: TextView = root.findViewById(R.id.statMode)

    var onAddCallClicked: Callback? = null
    var onHeroPressed: Callback? = null
    var onPingPressed: Callback? = null
    var onTunnelDiagnosticsPressed: Callback? = null
    var onDiscoveryRefreshPressed: Callback? = null
    var onServerChoiceClicked: ParamCallback<String>? = null
    var onSpeedTestPressed: Callback? = null
    var onCallSelected: ParamCallback<CallConfig>? = null
    var onCallLongPressed: ParamCallback<CallConfig>? = null

    private var pulseAnimator: ValueAnimator? = null
    private var pulseRunning: Boolean = false
    private var collapsedToActive: Boolean = false
    private var currentCalls: List<CallConfig> = emptyList()
    private var heroConnected = false
    private var activeCallId: String = ""

    init {
        emptyCta.clipToOutline = true
        pingButton.clipToOutline = true
        copySocksButton.clipToOutline = true
        speedTestButton.clipToOutline = true
        speedTestButton.visibility = View.GONE
        addButton.setOnClickListener { onAddCallClicked?.invoke() }
        emptyCta.setOnClickListener { onAddCallClicked?.invoke() }
        hero.setOnTouchListener { v, event ->
            when (event.action) {
                MotionEvent.ACTION_DOWN -> {
                    v.animate().scaleX(0.92f).scaleY(0.92f)
                        .setDuration(100)
                        .setInterpolator(OvershootInterpolator(2f))
                        .start()
                    true
                }
                MotionEvent.ACTION_UP -> {
                    v.animate().scaleX(1f).scaleY(1f)
                        .setDuration(200)
                        .setInterpolator(OvershootInterpolator(3f))
                        .start()
                    v.performHapticFeedback(HapticFeedbackConstants.VIRTUAL_KEY)
                    onHeroPressed?.invoke()
                    v.performClick()
                    true
                }
                MotionEvent.ACTION_CANCEL -> {
                    v.animate().scaleX(1f).scaleY(1f)
                        .setDuration(150)
                        .start()
                    true
                }
                else -> false
            }
        }
        pingButton.setOnClickListener { onPingPressed?.invoke() }
        copySocksButton.setOnClickListener { onTunnelDiagnosticsPressed?.invoke() }
        roomRefreshButton.setOnClickListener { onDiscoveryRefreshPressed?.invoke() }
        speedTestButton.setOnClickListener { onSpeedTestPressed?.invoke() }
    }

    fun bindCalls(calls: List<CallConfig>, activeId: String) {
        currentCalls = calls
        activeCallId = activeId
        renderCalls()
        updateHeaderSubForList(calls.size)
    }

    fun bindHero(connected: Boolean, status: VpnStatus?) {
        heroConnected = connected
        val context = root.context
        if (connected) {
            heroLabel.text = context.getString(R.string.hero_disconnect)
            hero.setBackgroundResource(R.drawable.bg_hero_connected)
            heroLabel.setTextColor(context.getColor(R.color.panel_bg))
            heroPowerIcon.setColorFilter(context.getColor(R.color.panel_bg))
            statusHeadline.text = context.getString(R.string.status_headline_connected)
            headerSub.text = context.getString(R.string.main_sub_live)
            statsCard.visibility = View.VISIBLE
            pingRow.visibility = View.GONE
            copySocksButton.visibility = View.VISIBLE
            speedTestButton.visibility = View.GONE
            emptyCta.visibility = View.GONE
            copySocksButtonLabel.text = context.getString(R.string.ping_run)
            heroRingOuter.applyState(HeroRingOuterView.State.CONNECTED)
            heroRingMid.setBackgroundResource(R.drawable.bg_hero_ring_dashed_active)
            statusDot.setBackgroundResource(R.drawable.bg_status_dot_active)
            collapsedToActive = true
            renderCalls()
            stopPulse()
        } else if (status == VpnStatus.CONNECTING || status == VpnStatus.STARTING || status == VpnStatus.STOPPING || status == VpnStatus.CALL_CONNECTED || status == VpnStatus.DATACHANNEL_OPEN) {
            heroLabel.text = context.getString(R.string.hero_cancel)
            hero.setBackgroundResource(R.drawable.bg_hero_active)
            heroLabel.setTextColor(UiColors.accent(context))
            heroPowerIcon.setColorFilter(UiColors.accent(context))
            statusHeadline.text = context.getString(if (status == VpnStatus.STOPPING) R.string.status_headline_disconnected else R.string.status_headline_connecting)
            statsCard.visibility = View.GONE
            pingRow.visibility = View.GONE
            copySocksButton.visibility = View.GONE
            speedTestButton.visibility = View.GONE
            speedTestButton.visibility = View.GONE
            heroRingOuter.applyState(HeroRingOuterView.State.CONNECTING)
            heroRingMid.setBackgroundResource(R.drawable.bg_hero_ring_dashed)
            statusDot.setBackgroundResource(R.drawable.bg_status_dot_warn)
            collapsedToActive = true
            renderCalls()
            startPulse()
        } else {
            heroLabel.text = context.getString(R.string.hero_connect)
            hero.setBackgroundResource(R.drawable.bg_hero_idle)
            heroLabel.setTextColor(context.getColor(R.color.ink_3))
            heroPowerIcon.setColorFilter(context.getColor(R.color.ink_3))
            statusHeadline.text = context.getString(R.string.status_headline_disconnected)
            statusDot.setBackgroundResource(R.drawable.bg_status_dot_idle)
            statusDetail.text = if (currentCalls.isEmpty()) {
                context.getString(R.string.status_detail_no_calls)
            } else {
                context.getString(R.string.status_detail_pick_call)
            }
            statsCard.visibility = View.GONE
            pingRow.visibility = View.GONE
            copySocksButton.visibility = View.GONE
            heroRingOuter.applyState(HeroRingOuterView.State.IDLE)
            heroRingMid.setBackgroundResource(R.drawable.bg_hero_ring_dashed)
            collapsedToActive = false
            renderCalls()
            updateHeaderSubForList(currentCalls.size)
            stopPulse()
            resetPingState()
        }
    }

    fun bindStatus(status: VpnStatus) {
        val labelRes = status.labelRes
        statusDetail.text = if (status == VpnStatus.PORT_BUSY) {
            root.context.getString(labelRes, Prefs.socksPort)
        } else {
            root.context.getString(labelRes)
        }
    }

    fun bindStatusText(text: String) {
        statusDetail.text = text
    }

    fun bindRoomWarmup(text: String, refreshing: Boolean) {
        roomWarmupStatus.text = text
        roomRefreshButton.alpha = if (refreshing) 0.55f else 1.0f
        roomRefreshButton.isEnabled = !refreshing
    }

    fun bindServerChoices(rooms: List<CallConfig>) {
        roomServerChoices.removeAllViews()
        if (rooms.isEmpty()) {
            roomServerChoices.visibility = View.GONE
            return
        }
        roomServerChoices.visibility = View.VISIBLE
        emptyCta.visibility = View.GONE
        rooms.distinctBy { it.url }.take(5).forEach { room ->
            val label = serverLabel(room)
            val chip = TextView(root.context).apply {
                text = label
                textSize = 12f
                setTextColor(root.context.getColor(R.color.accent_emerald))
                setBackgroundResource(R.drawable.bg_ping_button)
                setPadding(dp(12), dp(8), dp(12), dp(8))
                setOnClickListener { onServerChoiceClicked?.invoke(room.url) }
            }
            val lp = LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT).apply {
                bottomMargin = dp(6)
            }
            roomServerChoices.addView(chip, lp)
        }
    }

    private fun serverLabel(room: CallConfig): String {
        val raw = room.nodeLabel ?: room.name
        val text = raw
        val flag = when {
            text.contains("germany", ignoreCase = true) || text.contains("de", ignoreCase = true) -> "🇩🇪"
            text.contains("russia", ignoreCase = true) || text.contains("ru", ignoreCase = true) -> "🇷🇺"
            else -> "🌐"
        }
        return "$flag  $text"
    }

    private fun dp(v: Int): Int = (v * root.resources.displayMetrics.density).toInt()


    fun setStats(uptimeText: String, mode: String) {
        statUptime.text = uptimeText
        statMode.text = mode
    }

    fun showPingRunning() {
        pingButtonLabel.text = root.context.getString(R.string.ping_running)
        val anim = AlphaAnimation(0.5f, 1.0f).apply {
            duration = 450
            repeatMode = AlphaAnimation.REVERSE
            repeatCount = AlphaAnimation.INFINITE
        }
        pingButton.startAnimation(anim)
    }

    fun showPingResult(success: Boolean, rttMs: Int) {
        pingButton.clearAnimation()
        pingButtonLabel.text = root.context.getString(R.string.ping_run)
        pingResult.visibility = View.VISIBLE
        val host = "t.me/Kuplinov_Telegram/1032"
        if (success) {
            pingResult.setBackgroundResource(R.drawable.bg_ping_result_ok)
            pingResultHost.setTextColor(UiColors.accent(root.context))
            pingResultRtt.setTextColor(UiColors.accent(root.context))
            pingResultHost.text = root.context.getString(R.string.ping_ok, host)
            pingResultRtt.text = root.context.getString(R.string.ping_ms, rttMs)
        } else {
            pingResult.setBackgroundResource(R.drawable.bg_ping_result_fail)
            pingResultHost.setTextColor(root.context.getColor(R.color.error_red))
            pingResultRtt.setTextColor(root.context.getColor(R.color.error_red))
            pingResultHost.text = root.context.getString(R.string.ping_fail, host)
            pingResultRtt.text = root.context.getString(R.string.ping_timeout)
        }
    }


    fun showTunnelDiagnosticsRunning() {
        copySocksButtonLabel.text = root.context.getString(R.string.diag_ping)
        val anim = AlphaAnimation(0.5f, 1.0f).apply {
            duration = 450
            repeatMode = AlphaAnimation.REVERSE
            repeatCount = AlphaAnimation.INFINITE
        }
        copySocksButton.startAnimation(anim)
    }

    fun showTunnelDiagnosticsProgress(text: String) {
        copySocksButtonLabel.text = text
        pingResult.visibility = View.VISIBLE
        pingResult.setBackgroundResource(R.drawable.bg_ping_result_ok)
        pingResultHost.setTextColor(UiColors.accent(root.context))
        pingResultRtt.setTextColor(UiColors.accent(root.context))
        pingResultHost.text = root.context.getString(R.string.diag_result_title)
        pingResultRtt.text = text
    }

    fun showTunnelDiagnosticsResult(text: String, ok: Boolean) {
        copySocksButton.clearAnimation()
        copySocksButtonLabel.text = root.context.getString(R.string.ping_run)
        showResult(text, ok, root.context.getString(R.string.diag_result_title))
    }

    fun showSpeedRunning() {
        return
        @Suppress("UNREACHABLE_CODE")
        speedTestButtonLabel.text = root.context.getString(R.string.speedtest_running)
        val anim = AlphaAnimation(0.5f, 1.0f).apply {
            duration = 450
            repeatMode = AlphaAnimation.REVERSE
            repeatCount = AlphaAnimation.INFINITE
        }
        speedTestButton.startAnimation(anim)
    }

    fun showSpeedResult(text: String, ok: Boolean) {
        speedTestButton.clearAnimation()
        speedTestButtonLabel.text = root.context.getString(R.string.speedtest_run)
        showResult(text, ok, root.context.getString(R.string.speedtest_result_title))
    }

    private fun showResult(text: String, ok: Boolean, title: String) {
        pingResult.visibility = View.VISIBLE
        if (ok) {
            pingResult.setBackgroundResource(R.drawable.bg_ping_result_ok)
            pingResultHost.setTextColor(UiColors.accent(root.context))
            pingResultRtt.setTextColor(UiColors.accent(root.context))
            pingResultHost.text = title
            pingResultRtt.text = text
        } else {
            pingResult.setBackgroundResource(R.drawable.bg_ping_result_fail)
            pingResultHost.setTextColor(root.context.getColor(R.color.error_red))
            pingResultRtt.setTextColor(root.context.getColor(R.color.error_red))
            pingResultHost.text = title
            pingResultRtt.text = text
        }
    }

    fun pauseAnimations() {
        heroRingOuter.pauseAnimation()
        pulseAnimator?.cancel()
        pulseAnimator = null
        pingButton.clearAnimation()
    }

    fun resumeAnimations() {
        heroRingOuter.resumeAnimation()
        if (pulseRunning) startPulse()
    }

    fun detach() {
        heroRingOuter.release()
        stopPulse()
        pingButton.clearAnimation()
    }

    private fun renderCalls() {
        callsList.removeAllViews()
        val inflater = LayoutInflater.from(root.context)
        val visibleCalls = if (collapsedToActive) {
            currentCalls.filter { it.id == activeCallId }
        } else {
            currentCalls
        }
        if (currentCalls.isEmpty()) {
            emptyCta.visibility = View.GONE
            callsList.visibility = View.GONE
            return
        }
        emptyCta.visibility = View.GONE
        callsList.visibility = View.VISIBLE
        visibleCalls.forEach { config ->
            val row = inflater.inflate(R.layout.item_call_row, callsList, false)
            row.clipToOutline = true
            bindRow(row, config, isActive = config.id == activeCallId)
            row.setOnClickListener { onCallSelected?.invoke(config) }
            row.setOnLongClickListener {
                onCallLongPressed?.invoke(config)
                true
            }
            callsList.addView(row)
        }
    }

    private fun bindRow(row: View, config: CallConfig, isActive: Boolean) {
        val context = row.context
        val nameView = row.findViewById<TextView>(R.id.rowName)
        val linkView = row.findViewById<TextView>(R.id.rowLink)
        val glyphView = row.findViewById<TextView>(R.id.rowGlyph)
        val glyphBox = row.findViewById<View>(R.id.rowGlyphBox)
        val statusDot = row.findViewById<View>(R.id.rowStatusDot)

        nameView.text = config.name
        linkView.text = config.url
        glyphView.text = config.platformGlyph

        if (isActive) {
            row.setBackgroundResource(R.drawable.bg_destination_card_active)
            statusDot.setBackgroundResource(R.drawable.bg_status_dot_active)
            glyphBox.setBackgroundResource(R.drawable.bg_glyph_chip_active)
            glyphView.setTextColor(context.getColor(R.color.panel_bg))
        } else {
            row.setBackgroundResource(R.drawable.bg_destination_card)
            statusDot.setBackgroundResource(R.drawable.bg_status_dot_idle)
            glyphBox.setBackgroundResource(R.drawable.bg_glyph_chip)
            nameView.setTextColor(context.getColor(R.color.ink))
            linkView.setTextColor(context.getColor(R.color.ink_3))
            glyphView.setTextColor(context.getColor(R.color.ink))
        }
    }

    private fun updateHeaderSubForList(count: Int) {
        if (collapsedToActive) return
        headerSub.text = when (count) {
            0 -> root.context.getString(R.string.main_sub_no_configs)
            else -> root.context.resources.getQuantityString(R.plurals.main_sub_count, count, count)
        }
    }

    private fun startPulse() {
        pulseRunning = true
        if (pulseAnimator?.isStarted == true) return
        heroPulse.visibility = View.VISIBLE
        heroPulse.scaleX = 1f
        heroPulse.scaleY = 1f
        heroPulse.alpha = 1f
        pulseAnimator = ValueAnimator.ofFloat(0f, 1f).apply {
            duration = 1600L
            repeatCount = ValueAnimator.INFINITE
            repeatMode = ValueAnimator.RESTART
            addUpdateListener {
                val progress = it.animatedValue as Float
                val scale = 1f + progress * 0.18f
                heroPulse.scaleX = scale
                heroPulse.scaleY = scale
                heroPulse.alpha = 1f - progress
            }
            start()
        }
    }

    private fun stopPulse() {
        pulseRunning = false
        pulseAnimator?.cancel()
        pulseAnimator = null
        heroPulse.visibility = View.GONE
        heroPulse.scaleX = 1f
        heroPulse.scaleY = 1f
        heroPulse.alpha = 1f
    }

    private fun resetPingState() {
        pingButton.clearAnimation()
        copySocksButton.clearAnimation()
        speedTestButton.clearAnimation()
        pingButtonLabel.text = root.context.getString(R.string.ping_run)
        copySocksButtonLabel.text = root.context.getString(R.string.ping_run)
        speedTestButtonLabel.text = root.context.getString(R.string.speedtest_run)
        pingResult.visibility = View.GONE
    }
}
