cmake_minimum_required(VERSION 3.18)
project(forja_agent LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

# jvmti.h is vendored into this directory (OpenJDK GPLv2 + Classpath Exception).
include_directories(${CMAKE_CURRENT_SOURCE_DIR})

add_library(forja-agent SHARED agent.cpp)

# Needed for __android_log_print.
find_library(log-lib log)
target_link_libraries(forja-agent ${log-lib})
