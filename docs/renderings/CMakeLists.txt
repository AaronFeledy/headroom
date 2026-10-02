cmake_minimum_required(VERSION 3.25)
project(HeadroomDocumentation LANGUAGES CXX)
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_AUTOMOC ON)
set(BUILD_TESTING OFF CACHE BOOL "" FORCE)
set(HEADROOM_WITH_KDE_TRAY OFF CACHE BOOL "" FORCE)
set(HEADROOM_WITH_LAYER_SHELL OFF CACHE BOOL "" FORCE)
get_filename_component(HEADROOM_ROOT "../.." ABSOLUTE)
find_package(Qt6 6.12 REQUIRED COMPONENTS Quick QuickControls2 Widgets Network Svg Test)
add_subdirectory("${HEADROOM_ROOT}/clients/desktop" desktop EXCLUDE_FROM_ALL)
qt_add_executable(headroom-doc-render render.cpp)
target_link_libraries(headroom-doc-render PRIVATE headroom-core Qt6::Widgets Qt6::QuickControls2)
target_compile_definitions(headroom-doc-render PRIVATE HEADROOM_ROOT="${HEADROOM_ROOT}")
qt_add_resources(headroom-doc-render provider-icons PREFIX "/provider-icons"
    BASE "${HEADROOM_ROOT}/clients/shared/provider-icons"
    FILES
    "${HEADROOM_ROOT}/clients/shared/provider-icons/claude.svg"
    "${HEADROOM_ROOT}/clients/shared/provider-icons/codex.svg"
    "${HEADROOM_ROOT}/clients/shared/provider-icons/cursor.svg"
    "${HEADROOM_ROOT}/clients/shared/provider-icons/grok.svg"
)