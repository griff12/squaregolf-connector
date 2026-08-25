# core.aar ships its own consumer rules (proguard.txt: -keep class go.** / mobile.**)
# and AGP applies them even for a plain files() dependency, so nothing is needed here
# for the gomobile classes. Verified: the minified release DEX keeps Lmobile/Mobile;,
# Lmobile/Connector;, Lmobile/StatusListener;, Lmobile/BleBridge;, Lgo/Seq; un-renamed.
#
# Belt and braces -- these classes are reached only from JNI, so R8 cannot see the edge.
-keep class go.** { *; }
-keep class mobile.** { *; }
-keepclasseswithmembernames class * { native <methods>; }
