package bypass.whitelist.util

import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.nio.charset.StandardCharsets
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Writes the app-private relay log with redaction and deterministic retention.
 *
 * Every writer instance uses the same process-wide file lock because the legacy
 * and Capacitor hosts can write the shared [file] concurrently. File failures
 * are intentionally allowed to propagate to the caller instead of dropping a
 * diagnostic silently.
 */
class LogWriter(
    cacheDir: File,
    private val maxDisplayLines: Int = DEFAULT_MAX_DISPLAY_LINES,
    private val maxFileBytes: Long = DEFAULT_MAX_FILE_BYTES,
    private val maxRotatedFiles: Int = DEFAULT_MAX_ROTATED_FILES,
) {

    private val logFile = File(cacheDir, "relay.log")
    private val displayLines = ArrayDeque<String>()
    private val dateFormat = SimpleDateFormat("HH:mm:ss.SSS", Locale.US)

    init {
        require(maxDisplayLines >= 0) { "maxDisplayLines must not be negative" }
        require(maxFileBytes > 0) { "maxFileBytes must be positive" }
        require(maxRotatedFiles >= 0) { "maxRotatedFiles must not be negative" }
    }

    val file: File get() = logFile

    /** Truncate the current log and remove all retained rotations. */
    @Synchronized
    fun reset() {
        synchronized(fileLock) {
            FileOutputStream(logFile, false).use { }
            deleteRotatedFilesLocked()
        }
        displayLines.clear()
    }

    /**
     * Redact, persist, and expose one diagnostic line.
     *
     * The returned line is redacted as well, so callers that immediately show
     * it in the UI do not bypass the persistence safety boundary.
     */
    @Synchronized
    fun append(msg: String): AppendResult {
        val line = redact("${dateFormat.format(Date())} $msg")
        synchronized(fileLock) {
            val bytes = boundedLineBytes(line)
            if (logFile.length() > 0L && logFile.length() + bytes.size > maxFileBytes) {
                rotateFilesLocked()
            }
            FileOutputStream(logFile, true).use { output -> output.write(bytes) }
        }
        displayLines.addLast(line)
        val evicted = displayLines.size > maxDisplayLines
        if (evicted) displayLines.removeFirst()
        return AppendResult(line, evicted)
    }

    @Synchronized
    fun displayText(): String = displayLines.joinToString("\n")

    /** Kept for existing lifecycle callers; append opens a fresh bounded write. */
    @Synchronized
    fun close() = Unit

    private fun boundedLineBytes(line: String): ByteArray {
        val full = (line + "\n").toByteArray(StandardCharsets.UTF_8)
        if (full.size <= maxFileBytes) return full

        val marker = "[TRUNCATED]\n".toByteArray(StandardCharsets.UTF_8)
        val available = maxFileBytes.toInt() - marker.size
        if (available <= 0) return marker.copyOf(maxFileBytes.toInt())

        val prefix = StringBuilder()
        var used = 0
        for (character in line) {
            val characterBytes = character.toString().toByteArray(StandardCharsets.UTF_8)
            if (used + characterBytes.size > available) break
            prefix.append(character)
            used += characterBytes.size
        }
        return (prefix.toString() + "[TRUNCATED]\n").toByteArray(StandardCharsets.UTF_8)
    }

    private fun rotateFilesLocked() {
        if (maxRotatedFiles == 0) {
            deleteFileLocked(logFile)
            return
        }
        for (index in maxRotatedFiles downTo 1) {
            val source = if (index == 1) logFile else rotatedFile(index - 1)
            val target = rotatedFile(index)
            deleteFileLocked(target)
            if (source.isFile && !source.renameTo(target)) {
                throw IOException("unable to rotate ${source.absolutePath} to ${target.absolutePath}")
            }
        }
    }

    private fun deleteRotatedFilesLocked() {
        for (index in 1..maxRotatedFiles) deleteFileLocked(rotatedFile(index))
    }

    private fun rotatedFile(index: Int): File = File("${logFile.absolutePath}.$index")

    private fun deleteFileLocked(target: File) {
        if (target.exists() && !target.delete()) {
            throw IOException("unable to remove ${target.absolutePath}")
        }
    }

    data class AppendResult(val line: String, val evicted: Boolean)

    companion object {
        const val DEFAULT_MAX_DISPLAY_LINES: Int = 100
        const val DEFAULT_MAX_FILE_BYTES: Long = 64 * 1024
        const val DEFAULT_MAX_ROTATED_FILES: Int = 1

        private const val MAX_LINE_CHARACTERS: Int = 2_000
        private val fileLock = Any()
        private val vkToken = Regex("vk1\\.[A-Za-z0-9._-]+")
        private val jwt = Regex("eyJ[A-Za-z0-9._-]{20,}")
        private val credential = Regex(
            """(?i)(\b(?:user[_-]?(?:access[_-]?token|refresh[_-]?token)|session[_-]?token|access[_-]?token|refresh[_-]?token|token|cookie(?:_header)?|authorization|proxy-authorization|password|secret|api[_-]?key|endpoint|url)\b\s*[=:]\s*(?:bearer\s+)?)(?:"[^"]*"|'[^']*'|[^\s,;}]+)""",
        )
        private val endpoint = Regex("""(?i)\b(?:vless|wbstream)://[^\s"']+""")

        /** Read only a bounded tail and redact credentials before it leaves the app. */
        fun readRedacted(file: File, maxLines: Int = 300): List<String> {
            require(maxLines >= 0) { "maxLines must not be negative" }
            if (maxLines == 0 || !file.isFile) return emptyList()
            synchronized(fileLock) {
                val tail = ArrayDeque<String>(maxLines)
                file.useLines { lines ->
                    lines.forEach { raw ->
                        tail.addLast(redact(raw))
                        if (tail.size > maxLines) tail.removeFirst()
                    }
                }
                return tail.toList()
            }
        }

        private fun redact(value: String): String = value
            .replace(vkToken, "[REDACTED_VK_TOKEN]")
            .replace(jwt, "[REDACTED_JWT]")
            .replace(credential, "$1[REDACTED]")
            .replace(endpoint, "[REDACTED_ENDPOINT]")
            .take(MAX_LINE_CHARACTERS)
    }
}
