import java.io.File

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
}


fun loadBrandDotEnv(file: File): Map<String, String> {
    if (!file.isFile) return emptyMap()
    return file.readLines().mapNotNull { raw ->
        val line = raw.trim()
        if (line.isEmpty() || line.startsWith("#") || !line.contains("=")) return@mapNotNull null
        val key = line.substringBefore("=").trim()
        val value = line.substringAfter("=").trim()
        key to value
    }.toMap()
}

fun brandEnvValue(dotEnv: Map<String, String>, vararg names: String): String? {
    for (name in names) {
        val fromProcess = System.getenv(name)?.trim()?.takeIf { it.isNotEmpty() }
        if (fromProcess != null) return fromProcess
        val fromFile = dotEnv[name]?.trim()?.takeIf { it.isNotEmpty() }
        if (fromFile != null) return fromFile
    }
    return null
}

val brandDotEnv = listOf(
    rootProject.file(".env"),
    rootProject.file("../.env")
).firstOrNull { it.isFile }?.let(::loadBrandDotEnv) ?: emptyMap()

val appBrandName = brandEnvValue(brandDotEnv, "APP_BRAND", "BRANDING", "BRAND_NAME", "APP_NAME") ?: "whitelistbypass"
val allowEmptyMobileSecrets = brandEnvValue(brandDotEnv, "ALLOW_EMPTY_MOBILE_SECRETS") == "1"
val allowNoAutoUpdate = brandEnvValue(brandDotEnv, "ALLOW_NO_AUTOUPDATE") == "1"
val useGoRuntimeAar = brandEnvValue(brandDotEnv, "WT_USE_GO_RUNTIME_AAR") == "1"
fun requiredMobileSecret(name: String, vararg aliases: String): String {
    val value = brandEnvValue(brandDotEnv, name, *aliases)
    if (!value.isNullOrBlank()) return value
    if (allowEmptyMobileSecrets) return ""
    throw GradleException("Missing required Android secret $name. Put it in android-app/.env or export it. Set ALLOW_EMPTY_MOBILE_SECRETS=1 only for intentionally empty/dev builds.")
}
fun requiredAndroidConfig(name: String, vararg aliases: String): String {
    val value = brandEnvValue(brandDotEnv, name, *aliases)
    if (!value.isNullOrBlank()) return value
    if (allowNoAutoUpdate) return ""
    throw GradleException("Missing required Android config $name. Put it in android-app/.env or export it. Set ALLOW_NO_AUTOUPDATE=1 only for intentionally disabled auto-updates.")
}
val wtbusKeyB64 = requiredMobileSecret("WTBUS_KEY_B64")
val wtbusKeyId = brandEnvValue(brandDotEnv, "WTBUS_KEY_ID") ?: "k1"
val vkBotToken = requiredMobileSecret("VK_BOT_TOKEN")
val vkBotPeerId = requiredMobileSecret("VK_BOT_PEER_ID")
val vkDiscoveryGroupId = brandEnvValue(brandDotEnv, "VK_DISCOVERY_GROUP_ID") ?: ""
val vkTelemetryPeerId = brandEnvValue(brandDotEnv, "VK_TELEMETRY_PEER_ID") ?: ""
val vkBusPeerIds = brandEnvValue(brandDotEnv, "VK_BUS_PEER_IDS") ?: vkBotPeerId
val vkLogPeerIds = brandEnvValue(brandDotEnv, "VK_LOG_PEER_IDS") ?: vkTelemetryPeerId
val okGraphToken = brandEnvValue(brandDotEnv, "OK_GRAPH_TOKEN", "OK_GRAPH_TOKEN_1") ?: ""
val okGraphChatId = brandEnvValue(brandDotEnv, "WT_OK_CHAT_ID") ?: "chat:PLACEHOLDER_INJECT_VIA_ENV"
val okGraphUserId = brandEnvValue(brandDotEnv, "OK_GRAPH_USER_ID") ?: "user:910509886088"
val androidUpdateUrl = requiredAndroidConfig("ANDROID_UPDATE_URL", "UPDATE_URL", "APP_UPDATE_URL")

val versionMajor = 0
val versionMinor = 5
val versionPatch = 1
val versionCodeBase = 500_000
val versionCodeBuildStride = 1_000
val versionCodeMaxPatch = 2_099_499
val versionBuild = System.getenv("BUILD_NUMBER")?.let { rawBuildNumber ->
    rawBuildNumber.toIntOrNull()
        ?: throw GradleException("BUILD_NUMBER must be an integer in 0 until $versionCodeBuildStride")
} ?: 0
if (versionBuild !in 0 until versionCodeBuildStride) {
    throw GradleException("BUILD_NUMBER must be in 0 until $versionCodeBuildStride")
}
if (versionPatch !in 0..versionCodeMaxPatch) {
    throw GradleException("versionPatch must be in 0..$versionCodeMaxPatch")
}

android {
    namespace = "bypass.whitelist"
    compileSdk {
        version = release(36)
    }

    defaultConfig {
        applicationId = "bypass.whitelist"
        minSdk = 33
        targetSdk = 36
        versionCode = versionCodeBase + versionPatch * versionCodeBuildStride + versionBuild
        versionName = "$versionMajor.$versionMinor.$versionPatch"

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        resValue("string", "app_name", appBrandName)
        manifestPlaceholders["appLabel"] = appBrandName
        manifestPlaceholders["tileLabel"] = appBrandName
        buildConfigField("String", "WTBUS_KEY_B64", "\"$wtbusKeyB64\"")
        buildConfigField("String", "WTBUS_KEY_ID", "\"$wtbusKeyId\"")
        buildConfigField("String", "VK_BOT_TOKEN", "\"$vkBotToken\"")
        buildConfigField("String", "VK_BOT_PEER_ID", "\"$vkBotPeerId\"")
        buildConfigField("String", "VK_DISCOVERY_GROUP_ID", "\"$vkDiscoveryGroupId\"")
        buildConfigField("String", "VK_TELEMETRY_PEER_ID", "\"$vkTelemetryPeerId\"")
        buildConfigField("String", "VK_BUS_PEER_IDS", "\"$vkBusPeerIds\"")
        buildConfigField("String", "VK_LOG_PEER_IDS", "\"$vkLogPeerIds\"")
        buildConfigField("String", "OK_GRAPH_TOKEN", "\"$okGraphToken\"")
        buildConfigField("String", "OK_GRAPH_CHAT_ID", "\"$okGraphChatId\"")
        buildConfigField("String", "OK_GRAPH_USER_ID", "\"$okGraphUserId\"")
        buildConfigField("String", "ANDROID_UPDATE_URL", "\"$androidUpdateUrl\"")
    }

    signingConfigs {
        getByName("debug") {
            storeFile = file("../debug.keystore")
            storePassword = "android"
            keyAlias = "debug"
            keyPassword = "android"
        }
    }

    buildTypes {
        debug {
            signingConfig = signingConfigs.getByName("debug")
        }
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName("debug")
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }
    compileOptions {
        // Bumped 11 → 21 for Capacitor: :capacitor-android and capacitor.build.gradle
        // compile at Java 21; matching here avoids a Kotlin/Java jvmTarget mismatch.
        sourceCompatibility = JavaVersion.VERSION_21
        targetCompatibility = JavaVersion.VERSION_21
    }
    kotlinOptions {
        jvmTarget = "21"
    }
    buildFeatures {
        buildConfig = true
    }
    packaging {
        jniLibs {
            keepDebugSymbols += listOf("**/libgojni.so", "**/librelay.so", "**/libsingbox.so")
        }
    }
    if (useGoRuntimeAar) {
        sourceSets.getByName("main").java.srcDir("src/goRuntimeStubs/java")
    }
}

repositories {
    flatDir { dirs("../capacitor-cordova-android-plugins/src/main/libs", "libs") }
}

dependencies {
    if (useGoRuntimeAar) {
        implementation(fileTree(mapOf("dir" to "libs", "include" to listOf("wt-runtime.aar"))))
    } else {
        implementation(fileTree(mapOf("dir" to "libs", "include" to listOf("*.aar"), "exclude" to listOf("wt-runtime.aar"))))
    }
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.appcompat)
    implementation(libs.material)
    implementation(libs.androidx.activity)
    implementation(libs.androidx.constraintlayout)
    implementation("androidx.viewpager2:viewpager2:1.1.0")
    // Capacitor embed (manual; no `cap add`).
    implementation(project(":capacitor-android"))
    implementation(project(":capacitor-cordova-android-plugins"))
    testImplementation(libs.junit)
    androidTestImplementation(libs.androidx.junit)
    androidTestImplementation(libs.androidx.espresso.core)
}

// Capacitor's generated build hooks (compileOptions Java 21 + cordova vars).
// Copied verbatim from `cap add`; do not edit capacitor.build.gradle by hand.
apply(from = "capacitor.build.gradle")
