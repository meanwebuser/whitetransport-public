// Top-level build file where you can add configuration options common to all sub-projects/modules.
plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.kotlin.android) apply false
}

// Capacitor embed: defines rootProject.ext.{compileSdkVersion,minSdkVersion,...}
// which the :capacitor-android / cordova plugin modules read via hasProperty().
apply(from = "variables.gradle")