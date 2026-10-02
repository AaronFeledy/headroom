cmake_minimum_required(VERSION 3.25)
project(HeadroomLinuxCompatibility LANGUAGES CXX)

# Compatibility entry point for existing source-build commands. New builds
# should use clients/desktop directly.
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY "${CMAKE_BINARY_DIR}")
add_subdirectory("${CMAKE_CURRENT_LIST_DIR}/../desktop" desktop)
