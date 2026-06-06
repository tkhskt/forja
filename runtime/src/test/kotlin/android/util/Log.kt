// Hand-written test shim for android.util.Log. The android.jar stub bundled
// with com.google.android:android throws RuntimeException("Stub!") on every
// call, so we replace it on the test classpath with this no-op (with stdout
// passthrough) implementation. Only the four levels the runtime touches are
// included.
package android.util

@Suppress("unused")
object Log {
    @JvmStatic fun d(tag: String, msg: String): Int { println("D/$tag: $msg"); return 0 }
    @JvmStatic fun i(tag: String, msg: String): Int { println("I/$tag: $msg"); return 0 }
    @JvmStatic fun w(tag: String, msg: String): Int { println("W/$tag: $msg"); return 0 }
    @JvmStatic fun w(tag: String, msg: String, t: Throwable): Int {
        println("W/$tag: $msg :: $t"); return 0
    }
    @JvmStatic fun e(tag: String, msg: String): Int { println("E/$tag: $msg"); return 0 }
}
