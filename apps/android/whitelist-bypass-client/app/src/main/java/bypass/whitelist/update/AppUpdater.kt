package bypass.whitelist.update

import bypass.whitelist.BuildConfig
import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.Settings
import android.widget.Toast
import androidx.core.content.FileProvider
import bypass.whitelist.R
import bypass.whitelist.util.Prefs
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import kotlin.concurrent.thread

object AppUpdater {
    private val UPDATE_URL: String = BuildConfig.ANDROID_UPDATE_URL
    private const val AUTO_CHECK_INTERVAL_MS = 6L * 60L * 60L * 1000L
    @Volatile private var running = false

    data class UpdateInfo(
        val versionName: String,
        val versionCode: Long,
        val apkUrl: String,
        val sha256: String,
        val notes: String,
        val mandatory: Boolean,
    )

    fun check(activity: Activity, manual: Boolean, logger: (String) -> Unit = {}) {
        if (UPDATE_URL.isBlank()) {
            logger("Update check skipped: ANDROID_UPDATE_URL is empty")
            return
        }
        if (!manual && System.currentTimeMillis() - Prefs.lastUpdateCheckMs < AUTO_CHECK_INTERVAL_MS) return
        if (running) return
        running = true
        if (manual) Toast.makeText(activity, R.string.update_checking, Toast.LENGTH_SHORT).show()
        logger("Update check started")
        thread(name = "app-updater") {
            try {
                Prefs.lastUpdateCheckMs = System.currentTimeMillis()
                val currentCode = currentVersionCode(activity)
                val info = fetchUpdateInfo()
                activity.runOnUiThread {
                    if (info.versionCode > currentCode) {
                        promptUpdate(activity, info, logger)
                    } else if (manual) {
                        Toast.makeText(activity, activity.getString(R.string.update_no_update, currentVersionName(activity)), Toast.LENGTH_LONG).show()
                    }
                    logger("Update check finished: current=$currentCode latest=${info.versionCode}")
                }
            } catch (e: Exception) {
                activity.runOnUiThread {
                    if (manual) Toast.makeText(activity, activity.getString(R.string.update_failed, e.message ?: "error"), Toast.LENGTH_LONG).show()
                    logger("Update check failed: ${e.message}")
                }
            } finally {
                running = false
            }
        }
    }

    private fun promptUpdate(activity: Activity, info: UpdateInfo, logger: (String) -> Unit) {
        AlertDialog.Builder(activity)
            .setTitle(activity.getString(R.string.update_available_title, info.versionName))
            .setMessage(activity.getString(R.string.update_available_message, info.notes.ifBlank { "BEZаботный-NET" }))
            .setPositiveButton(R.string.update_install) { _, _ -> downloadAndInstall(activity, info, logger) }
            .setNegativeButton(if (info.mandatory) R.string.update_later else android.R.string.cancel, null)
            .show()
    }

    private fun downloadAndInstall(activity: Activity, info: UpdateInfo, logger: (String) -> Unit) {
        if (Build.VERSION.SDK_INT >= 26 && !activity.packageManager.canRequestPackageInstalls()) {
            AlertDialog.Builder(activity)
                .setTitle(R.string.update_allow_unknown_title)
                .setMessage(R.string.update_allow_unknown_message)
                .setPositiveButton(R.string.update_open_settings) { _, _ ->
                    val intent = Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${activity.packageName}"))
                    activity.startActivity(intent)
                }
                .setNegativeButton(android.R.string.cancel, null)
                .show()
            return
        }
        Toast.makeText(activity, R.string.update_downloading, Toast.LENGTH_SHORT).show()
        logger("Update download started: ${info.versionName}")
        thread(name = "app-update-download") {
            try {
                val file = downloadApk(activity, info)
                if (info.sha256.isNotBlank()) verifySha256(file, info.sha256)
                activity.runOnUiThread {
                    logger("Update downloaded: ${file.name}")
                    installApk(activity, file)
                }
            } catch (e: Exception) {
                activity.runOnUiThread {
                    Toast.makeText(activity, activity.getString(R.string.update_failed, e.message ?: "error"), Toast.LENGTH_LONG).show()
                    logger("Update download failed: ${e.message}")
                }
            }
        }
    }

    private fun fetchUpdateInfo(): UpdateInfo {
        val text = httpGetText(UPDATE_URL)
        val json = JSONObject(text)
        return UpdateInfo(
            versionName = json.optString("versionName", json.optString("version")),
            versionCode = json.getLong("versionCode"),
            apkUrl = json.optString("apkUrl", json.optString("url")),
            sha256 = json.optString("sha256"),
            notes = json.optString("notes"),
            mandatory = json.optBoolean("mandatory", false),
        )
    }

    private fun httpGetText(url: String): String {
        val conn = URL(url).openConnection() as HttpURLConnection
        conn.connectTimeout = 8000
        conn.readTimeout = 12000
        conn.setRequestProperty("User-Agent", "BEZabotny-NET updater")
        return conn.inputStream.bufferedReader(Charsets.UTF_8).use { it.readText() }
    }

    private fun downloadApk(activity: Activity, info: UpdateInfo): File {
        val dir = File(activity.cacheDir, "updates").also { it.mkdirs() }
        val file = File(dir, "BEZabotny-NET-${info.versionCode}.apk")
        val conn = URL(info.apkUrl).openConnection() as HttpURLConnection
        conn.connectTimeout = 10000
        conn.readTimeout = 60000
        conn.setRequestProperty("User-Agent", "BEZabotny-NET updater")
        conn.inputStream.use { input -> file.outputStream().use { output -> input.copyTo(output) } }
        return file
    }

    private fun installApk(activity: Activity, file: File) {
        val uri = FileProvider.getUriForFile(activity, "${activity.packageName}.fileprovider", file)
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        activity.startActivity(intent)
    }

    private fun verifySha256(file: File, expected: String) {
        val md = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buf = ByteArray(64 * 1024)
            while (true) {
                val n = input.read(buf)
                if (n <= 0) break
                md.update(buf, 0, n)
            }
        }
        val actual = md.digest().joinToString("") { "%02x".format(it) }
        if (!actual.equals(expected.trim(), ignoreCase = true)) throw IllegalStateException("SHA256 mismatch")
    }

    @Suppress("DEPRECATION")
    private fun currentVersionCode(activity: Activity): Long {
        val info = activity.packageManager.getPackageInfo(activity.packageName, 0)
        return if (Build.VERSION.SDK_INT >= 28) info.longVersionCode else info.versionCode.toLong()
    }

    private fun currentVersionName(activity: Activity): String {
        return activity.packageManager.getPackageInfo(activity.packageName, 0).versionName ?: "unknown"
    }
}
