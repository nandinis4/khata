# gomobile generates JNI bindings that are reached reflectively from native
# code. Stripping or renaming them breaks the engine at runtime with an error
# that looks nothing like its cause, so keep the whole binding surface.
-keep class dev.khata.engine.** { *; }
-keep class go.** { *; }

# kotlinx.serialization puts generated serializers on the companion object.
-keepattributes *Annotation*, InnerClasses
-dontnote kotlinx.serialization.**
-keepclassmembers class dev.khata.app.** {
    *** Companion;
}
-keepclasseswithmembers class dev.khata.app.** {
    kotlinx.serialization.KSerializer serializer(...);
}
