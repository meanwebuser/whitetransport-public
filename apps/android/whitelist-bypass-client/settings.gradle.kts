pluginManagement {
    repositories {
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("com\\.google.*")
                includeGroupByRegex("androidx.*")
            }
        }
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    // Capacitor's :capacitor-android / :capacitor-cordova-android-plugins modules
    // declare their own repositories{} (google/mavenCentral/flatDir), which
    // FAIL_ON_PROJECT_REPOS forbids. Relax to PREFER_SETTINGS so the root repos
    // win but per-project repos are tolerated (matches Capacitor's own template).
    repositoriesMode.set(RepositoriesMode.PREFER_SETTINGS)
    repositories {
        google()
        mavenCentral()
        flatDir { dirs("$rootDir/capacitor-cordova-android-plugins/src/main/libs", "$rootDir/app/libs") }
    }
}

rootProject.name = "WhitelistBypass"
include(":app")

// ── Capacitor embed (manual; we do NOT use `cap add`) ───────────────────────
// node_modules is hoisted to the monorepo root, three levels up from here:
//   apps/android/whitelist-bypass-client → apps/android → apps → <root>
val capacitorAndroidDir = file("../../../node_modules/@capacitor/android/capacitor")
include(":capacitor-android")
project(":capacitor-android").projectDir = capacitorAndroidDir

include(":capacitor-cordova-android-plugins")
project(":capacitor-cordova-android-plugins").projectDir = file("./capacitor-cordova-android-plugins/")
 