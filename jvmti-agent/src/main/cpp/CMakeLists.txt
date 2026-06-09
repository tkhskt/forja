cmake_minimum_required(VERSION 3.18)
project(forja_agent LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

# jvmti.h is vendored into this directory (OpenJDK GPLv2 + Classpath Exception).
include_directories(${CMAKE_CURRENT_SOURCE_DIR})

# ---------------------------------------------------------------------------
# slicer (AOSP dexter, Apache-2.0) — vendored under slicer/.
# Used to instrument okhttp3.OkHttpClient.interceptors() via an exit hook,
# the same dex-bytecode-rewriting technique Android Studio's NetworkInspector
# (ArtTooling) uses. Headers live under slicer/export/slicer/.
# ---------------------------------------------------------------------------
file(GLOB SLICER_SRC ${CMAKE_CURRENT_SOURCE_DIR}/slicer/*.cc)
add_library(slicer STATIC ${SLICER_SRC})
target_include_directories(slicer PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/slicer/export)

# slicer's dex_format.cc computes an Adler-32 checksum via zlib.
find_library(z-lib z)
target_link_libraries(slicer ${z-lib})

add_library(forja-agent SHARED agent.cpp)
target_include_directories(forja-agent PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/slicer/export)

# Needed for __android_log_print.
find_library(log-lib log)
target_link_libraries(forja-agent slicer ${log-lib} ${z-lib})
