import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.zip.ZipFile

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
}

android {
    namespace = "com.squaregolf.connector"
    compileSdk = 35
    buildToolsVersion = "35.0.0"
    ndkVersion = "27.2.12479018"

    defaultConfig {
        applicationId = "com.squaregolf.connector"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.1-phase1"

        // core.aar ships arm64-v8a only. Without this, androidx.graphics:graphics-path
        // contributes armeabi-v7a/x86/x86_64 .so files, the APK installs on an x86_64
        // emulator, and System.loadLibrary("gojni") then fails at runtime. With it, a
        // non-arm64 install fails loudly with INSTALL_FAILED_NO_MATCHING_ABIS.
        ndk { abiFilters += "arm64-v8a" }
    }

    buildTypes {
        debug {
            isMinifyEnabled = false
        }
        release {
            isMinifyEnabled = true
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
        }
    }

    splits {
        abi { isEnable = false }
    }

    packaging {
        jniLibs {
            useLegacyPackaging = false
            // Package libgojni.so unstripped: Go's symbol table is what makes a native
            // tombstone readable. Also silences AGP's "Unable to strip" notice.
            keepDebugSymbols += "**/libgojni.so"
        }
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlin {
        compilerOptions {
            jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
        }
    }

    buildFeatures { compose = true }
}

dependencies {
    // The gomobile AAR. Do NOT replace with a flatDir repository: settings.gradle.kts
    // sets FAIL_ON_PROJECT_REPOS and the build will fail. Do NOT replace with
    // fileTree(include = listOf("*.jar", "*.aar")) either: app/libs also holds
    // core-sources.jar, which would land on the compile classpath.
    implementation(files("libs/core.aar"))

    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.graphics)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.kotlinx.coroutines.android)

    debugImplementation(libs.androidx.compose.ui.tooling)
}

// ---------------------------------------------------------------------------
// verifyApkAbi -- build-time guard against the two silent packaging failures.
//
//  1. Wrong ABI set. An APK carrying a non-arm64 .so installs on a device that has
//     no libgojni.so for it and dies at System.loadLibrary.
//  2. A 4 KB-aligned libgojni.so. useLegacyPackaging=false maps the .so straight
//     out of the APK, so on a 16 KB-page device (Android 15+) dlopen refuses it.
//     Fix by rebuilding the AAR with
//       -ldflags="-extldflags=-Wl,-z,max-page-size=16384"
// ---------------------------------------------------------------------------
fun minPtLoadAlign(elf: ByteArray): Long {
    require(elf.size > 64) { "not an ELF" }
    require(elf[0] == 0x7f.toByte() && elf[1] == 'E'.code.toByte() &&
            elf[2] == 'L'.code.toByte() && elf[3] == 'F'.code.toByte()) { "bad ELF magic" }
    require(elf[4] == 2.toByte()) { "not ELF64" }
    val bb = ByteBuffer.wrap(elf).order(ByteOrder.LITTLE_ENDIAN)
    val phoff = bb.getLong(0x20)
    val phentsize = bb.getShort(0x36).toInt() and 0xffff
    val phnum = bb.getShort(0x38).toInt() and 0xffff
    var min = Long.MAX_VALUE
    for (i in 0 until phnum) {
        val o = (phoff + i.toLong() * phentsize).toInt()
        if (bb.getInt(o) == 1) {                       // PT_LOAD
            min = minOf(min, bb.getLong(o + 0x30))     // p_align
        }
    }
    require(min != Long.MAX_VALUE) { "no PT_LOAD segments" }
    return min
}

val rebuildHint =
    "Rebuild the AAR with: gomobile bind -androidapi 21 -target=android/arm64 " +
    "-ldflags=\"-extldflags=-Wl,-z,max-page-size=16384\" -o app/libs/core.aar ./mobile"

androidComponents.onVariants { variant ->
    val verify = tasks.register("verify${variant.name.replaceFirstChar { it.uppercase() }}ApkAbi") {
        group = "verification"
        description = "Assert the ${variant.name} APK carries exactly arm64-v8a and a 16 KB-aligned libgojni.so"
        val apkDir = variant.artifacts.get(com.android.build.api.artifact.SingleArtifact.APK)
        inputs.dir(apkDir)
        outputs.upToDateWhen { false }
        doLast {
            val dir = apkDir.get().asFile
            val apk = dir.listFiles { f -> f.name.endsWith(".apk") }?.firstOrNull()
                ?: throw GradleException("verifyApkAbi: no APK found in $dir")
            ZipFile(apk).use { zip ->
                val libs = zip.entries().toList().map { it.name }.filter { it.startsWith("lib/") }
                val abis = libs.map { it.removePrefix("lib/").substringBefore('/') }.toSet()
                if (abis != setOf("arm64-v8a")) {
                    throw GradleException("verifyApkAbi: expected exactly [arm64-v8a], APK has $abis (entries=$libs)")
                }
                val goEntry = zip.getEntry("lib/arm64-v8a/libgojni.so")
                    ?: throw GradleException("verifyApkAbi: lib/arm64-v8a/libgojni.so missing. $rebuildHint")
                val bytes = zip.getInputStream(goEntry).use { it.readBytes() }
                val align = minPtLoadAlign(bytes)
                if (align < 16384L) {
                    throw GradleException(
                        "verifyApkAbi: libgojni.so minimum PT_LOAD p_align is $align, need >= 16384. " +
                        "It will not dlopen on a 16 KB-page device. $rebuildHint"
                    )
                }
                logger.lifecycle("verifyApkAbi OK: ${apk.name}, ${bytes.size} B libgojni.so, p_align=$align")
            }
        }
    }
    tasks.matching { it.name == "assemble${variant.name.replaceFirstChar { c -> c.uppercase() }}" }
        .configureEach { finalizedBy(verify) }
}
