cmake_minimum_required(VERSION 3.21.1)
set(HEADROOM_VERSION "0.1.0" CACHE STRING "Headroom application version")
if(NOT HEADROOM_VERSION MATCHES "^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(\\+[0-9A-Za-z-]+(\\.[0-9A-Za-z-]+)*)?$")
    message(FATAL_ERROR "HEADROOM_VERSION must be stable SemVer (build metadata is allowed)")
endif()
set(HEADROOM_VERSION_MAJOR "${CMAKE_MATCH_1}")
set(HEADROOM_VERSION_MINOR "${CMAKE_MATCH_2}")
set(HEADROOM_VERSION_PATCH "${CMAKE_MATCH_3}")
foreach(component IN ITEMS HEADROOM_VERSION_MAJOR HEADROOM_VERSION_MINOR HEADROOM_VERSION_PATCH)
    if(${component} GREATER 65534)
        message(FATAL_ERROR "HEADROOM_VERSION numeric components must fit .NET and Windows version resources")
    endif()
endforeach()
set(HEADROOM_NUMERIC_VERSION "${HEADROOM_VERSION_MAJOR}.${HEADROOM_VERSION_MINOR}.${HEADROOM_VERSION_PATCH}")
if(CMAKE_HOST_APPLE OR CMAKE_SYSTEM_NAME STREQUAL "Darwin")
    if(CMAKE_OSX_DEPLOYMENT_TARGET AND CMAKE_OSX_DEPLOYMENT_TARGET VERSION_LESS "12.0")
        message(FATAL_ERROR "Headroom requires macOS 12.0 or newer")
    endif()
    if(NOT CMAKE_OSX_DEPLOYMENT_TARGET)
        set(CMAKE_OSX_DEPLOYMENT_TARGET "12.0" CACHE STRING "Minimum macOS version" FORCE)
    endif()
endif()
project(Headroom LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

option(HEADROOM_WITH_KDE_TRAY "Use KDE StatusNotifierItem integration when available" ON)
option(HEADROOM_WITH_LAYER_SHELL "Use LayerShellQt popup placement when available" ON)
option(HEADROOM_PACKAGE_BUILD "Install a relocatable package runtime" OFF)
option(HEADROOM_SYSTEM_MANAGED "Use system package-manager update guidance" OFF)

if(HEADROOM_PACKAGE_BUILD AND NOT APPLE)
    set(QT_DEPLOY_PLUGINS_DIR "plugins")
    set(QT_DEPLOY_QML_DIR "qml")
    find_program(HEADROOM_PATCHELF patchelf)
    if(HEADROOM_PATCHELF)
        set(QT_DEPLOY_USE_PATCHELF TRUE)
        set(QT_DEPLOY_PATCHELF_EXECUTABLE "${HEADROOM_PATCHELF}")
    endif()
endif()

if(HEADROOM_PACKAGE_BUILD AND CMAKE_SYSTEM_NAME STREQUAL "Linux")
    set(CMAKE_INSTALL_RPATH "$ORIGIN/../lib")
    set(CMAKE_BUILD_WITH_INSTALL_RPATH OFF)
endif()

if(APPLE)
    set(HEADROOM_QT_MINIMUM 6.8.3)
else()
    set(HEADROOM_QT_MINIMUM 6.6)
endif()
find_package(Qt6 ${HEADROOM_QT_MINIMUM} REQUIRED COMPONENTS Quick QuickControls2 Widgets Network Svg Test)
if(HEADROOM_PACKAGE_BUILD AND CMAKE_SYSTEM_NAME STREQUAL "Linux")
    find_package(Qt6 6.6 REQUIRED COMPONENTS WaylandClient)
endif()
configure_file(build-metadata.json.in "${CMAKE_CURRENT_BINARY_DIR}/headroom-build-metadata.json" @ONLY)
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
    if(HEADROOM_WITH_LAYER_SHELL)
        find_package(LayerShellQt 6.6 QUIET)
    endif()
    if(HEADROOM_WITH_KDE_TRAY)
        find_package(KF6StatusNotifierItem QUIET)
    endif()
endif()

qt_standard_project_setup(REQUIRES ${HEADROOM_QT_MINIMUM})
if(Qt6_VERSION VERSION_GREATER_EQUAL 6.8)
    qt_policy(SET QTP0004 NEW)
endif()
if(MSVC)
    # Source and generated QML type-registration code contain Unicode labels.
    add_compile_options(/utf-8)
endif()

qt_add_library(headroom-core STATIC
    controller.cpp resetaction.cpp usage.cpp warning.cpp trayvisual.cpp trayattention.cpp stabletray.cpp startup.cpp appinfo.cpp updateservice.cpp remoteupdate.cpp popup.cpp displayplatform.cpp
    settings.cpp instance.cpp managedserver.cpp jobobject.cpp credentialservice.cpp sshnetwork.cpp)
target_link_libraries(headroom-core PUBLIC Qt6::Quick Qt6::Network Qt6::Svg)
target_include_directories(headroom-core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_compile_definitions(headroom-core PUBLIC HEADROOM_VERSION="${HEADROOM_VERSION}")
if(HEADROOM_SYSTEM_MANAGED)
    target_compile_definitions(headroom-core PUBLIC HEADROOM_SYSTEM_MANAGED)
endif()
if(CMAKE_SYSTEM_NAME STREQUAL "Linux" AND HEADROOM_WITH_LAYER_SHELL AND LayerShellQt_FOUND)
    target_link_libraries(headroom-core PUBLIC LayerShellQt::Interface)
    target_compile_definitions(headroom-core PRIVATE HEADROOM_LAYER_SHELL)
endif()

set(headroom_platform_sources)
if(WIN32)
    target_link_libraries(headroom-core PUBLIC user32)
    set(HEADROOM_WINDOWS_ICON "${CMAKE_CURRENT_SOURCE_DIR}/windows/headroom.ico")
    set(HEADROOM_WINDOWS_MANIFEST "${CMAKE_CURRENT_BINARY_DIR}/headroom.manifest")
    configure_file(windows/headroom.manifest "${HEADROOM_WINDOWS_MANIFEST}" @ONLY)
    if(MSVC)
        # MSVC creates manifest resource ID 1 itself. Supply our settings as a
        # linker merge input instead of compiling a second ID 1 into the RC.
        set(HEADROOM_MANIFEST_RESOURCE "")
    else()
        set(HEADROOM_MANIFEST_RESOURCE
            "CREATEPROCESS_MANIFEST_RESOURCE_ID RT_MANIFEST \"${HEADROOM_WINDOWS_MANIFEST}\"")
    endif()
    configure_file(windows/headroom.rc.in "${CMAKE_CURRENT_BINARY_DIR}/headroom.rc" @ONLY)
    set_property(SOURCE "${CMAKE_CURRENT_BINARY_DIR}/headroom.rc" APPEND PROPERTY
        OBJECT_DEPENDS "${HEADROOM_WINDOWS_ICON}")
    list(APPEND headroom_platform_sources "${CMAKE_CURRENT_BINARY_DIR}/headroom.rc")
elseif(APPLE)
    set(HEADROOM_MACOS_ICON "${CMAKE_CURRENT_SOURCE_DIR}/macos/headroom.icns")
    set_source_files_properties("${HEADROOM_MACOS_ICON}" PROPERTIES MACOSX_PACKAGE_LOCATION Resources)
    list(APPEND headroom_platform_sources "${HEADROOM_MACOS_ICON}")
    configure_file(macos/Info.plist.in "${CMAKE_CURRENT_BINARY_DIR}/Info.plist" @ONLY)
endif()
qt_add_executable(headroom WIN32 main.cpp ${headroom_platform_sources})
if(APPLE)
    set_target_properties(headroom PROPERTIES
        MACOSX_BUNDLE TRUE
        MACOSX_BUNDLE_INFO_PLIST "${CMAKE_CURRENT_BINARY_DIR}/Info.plist")
endif()
if(MSVC)
    target_link_options(headroom PRIVATE
        "/MANIFEST:EMBED"
        "/MANIFESTINPUT:${HEADROOM_WINDOWS_MANIFEST}")
endif()
if(CMAKE_SYSTEM_NAME STREQUAL "Linux" AND HEADROOM_WITH_KDE_TRAY AND KF6StatusNotifierItem_FOUND)
    target_link_libraries(headroom PRIVATE KF6::StatusNotifierItem)
    target_compile_definitions(headroom PRIVATE HEADROOM_KDE_TRAY)
endif()
target_link_libraries(headroom PRIVATE headroom-core Qt6::Widgets Qt6::QuickControls2 Qt6::Svg)
if(HEADROOM_PACKAGE_BUILD AND CMAKE_SYSTEM_NAME STREQUAL "Linux")
    target_link_libraries(headroom PRIVATE Qt6::WaylandClient)
endif()

set_source_files_properties(qml/Theme.qml PROPERTIES QT_QML_SINGLETON_TYPE TRUE)
qt_add_qml_module(headroom URI Headroom VERSION 1.0 QML_FILES
    qml/Theme.qml qml/Main.qml qml/ActionButton.qml qml/ProviderCard.qml qml/Meter.qml qml/SettingsPanel.qml qml/StartupSettings.qml qml/AppInfoSettings.qml qml/DiagnosticsPanel.qml qml/BasicStyleDependency.qml
    RESOURCES headroom.svg)
qt_add_resources(headroom provider-icons PREFIX "/provider-icons" BASE "../shared/provider-icons" FILES
    ../shared/provider-icons/claude.svg ../shared/provider-icons/codex.svg
    ../shared/provider-icons/cursor.svg ../shared/provider-icons/grok.svg)

include(CTest)
if(BUILD_TESTING)
    if(APPLE)
        set(HEADROOM_MACOS_TLS_FIXTURE_DIR "${CMAKE_CURRENT_BINARY_DIR}/headroom-tls-fixture")
        set(HEADROOM_MACOS_TLS_FIXTURE_HEADER
            "${HEADROOM_MACOS_TLS_FIXTURE_DIR}/headroom_macos_tls_fixture.h")
        if(NOT EXISTS "/usr/bin/openssl")
            message(FATAL_ERROR "The macOS TLS fixtures require /usr/bin/openssl")
        endif()
        add_custom_target(headroom-macos-tls-fixture
            COMMAND "${CMAKE_COMMAND}"
                -DOPENSSL_EXECUTABLE=/usr/bin/openssl
                -DOUTPUT_DIRECTORY=${HEADROOM_MACOS_TLS_FIXTURE_DIR}
                -P "${CMAKE_CURRENT_SOURCE_DIR}/tests/generate_macos_tls_fixture.cmake"
            BYPRODUCTS
                "${HEADROOM_MACOS_TLS_FIXTURE_HEADER}"
                "${HEADROOM_MACOS_TLS_FIXTURE_DIR}/primary-certificate.pem"
                "${HEADROOM_MACOS_TLS_FIXTURE_DIR}/primary-private-key.pem"
                "${HEADROOM_MACOS_TLS_FIXTURE_DIR}/replacement-certificate.pem"
                "${HEADROOM_MACOS_TLS_FIXTURE_DIR}/replacement-private-key.pem"
            VERBATIM)
    endif()
    qt_add_executable(headroom-ssh-probe tests/ssh_probe.cpp)
    set_target_properties(headroom-ssh-probe PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-ssh-probe PRIVATE headroom-core Qt6::Network)
    qt_add_executable(headroom-ssh-fixture tests/ssh_fixture.cpp)
    set_target_properties(headroom-ssh-fixture PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-ssh-fixture PRIVATE Qt6::Core)
    qt_add_executable(headroom-ssh-tests tests/test_sshnetwork.cpp)
    set_target_properties(headroom-ssh-tests PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-ssh-tests PRIVATE headroom-core Qt6::Test Qt6::Network)
    target_compile_definitions(headroom-ssh-tests PRIVATE SSH_FIXTURE_PATH="$<TARGET_FILE:headroom-ssh-fixture>")
    add_dependencies(headroom-ssh-tests headroom-ssh-fixture)
    add_test(NAME headroom-ssh-tests COMMAND headroom-ssh-tests -o -,txt)
    qt_add_executable(headroom-managed-server-fixture tests/managed_server_fixture.cpp)
    set_target_properties(headroom-managed-server-fixture PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-managed-server-fixture PRIVATE Qt6::Network)
    qt_add_executable(headroom-credential-helper-fixture tests/credential_helper_fixture.cpp)
    set_target_properties(headroom-credential-helper-fixture PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-credential-helper-fixture PRIVATE Qt6::Core)
    qt_add_executable(headroom-managed-server-owner tests/managed_server_owner.cpp)
    set_target_properties(headroom-managed-server-owner PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-managed-server-owner PRIVATE headroom-core)
    foreach(feature stabletray startup appinfo update remoteupdate tray trayattention popup displayplatform settings instance)
        qt_add_executable(headroom-${feature}-tests tests/test_${feature}.cpp)
        set_target_properties(headroom-${feature}-tests PROPERTIES WIN32_EXECUTABLE FALSE)
        target_link_libraries(headroom-${feature}-tests PRIVATE headroom-core Qt6::Test Qt6::Widgets)
        add_test(NAME headroom-${feature}-tests COMMAND headroom-${feature}-tests
            -o -,txt -o "${CMAKE_CURRENT_BINARY_DIR}/headroom-${feature}-tests.xml,junitxml")
        set_tests_properties(headroom-${feature}-tests PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen")
    endforeach()
    target_compile_definitions(headroom-appinfo-tests PRIVATE SSH_FIXTURE_PATH="$<TARGET_FILE:headroom-ssh-fixture>")
    add_dependencies(headroom-appinfo-tests headroom-ssh-fixture)
    qt_add_executable(headroom-update-fixture tests/update_fixture.cpp)
    set_target_properties(headroom-update-fixture PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-update-fixture PRIVATE Qt6::Core)
    target_compile_definitions(headroom-update-tests PRIVATE UPDATE_FIXTURE_PATH="$<TARGET_FILE:headroom-update-fixture>")
    add_dependencies(headroom-update-tests headroom-update-fixture)
    qt_add_executable(headroom-remote-update-fixture tests/remote_update_fixture.cpp)
    set_target_properties(headroom-remote-update-fixture PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-remote-update-fixture PRIVATE Qt6::Core)
    target_compile_definitions(headroom-remoteupdate-tests PRIVATE REMOTE_UPDATE_FIXTURE_PATH="$<TARGET_FILE:headroom-remote-update-fixture>")
    add_dependencies(headroom-remoteupdate-tests headroom-remote-update-fixture)
    qt_add_executable(headroom-instance-helper tests/instance_helper.cpp)
    set_target_properties(headroom-instance-helper PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-instance-helper PRIVATE headroom-core)
    add_dependencies(headroom-instance-tests headroom-instance-helper)
    qt_add_resources(headroom-tray-tests provider-icons PREFIX "/provider-icons" BASE "../shared/provider-icons" FILES
        ../shared/provider-icons/claude.svg ../shared/provider-icons/codex.svg
        ../shared/provider-icons/cursor.svg ../shared/provider-icons/grok.svg)
    qt_add_executable(headroom-tests tests/test_usage.cpp)
    set_target_properties(headroom-tests PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-tests PRIVATE headroom-core Qt6::Test)
    target_compile_definitions(headroom-tests PRIVATE
        MANAGED_FIXTURE_PATH="$<TARGET_FILE:headroom-managed-server-fixture>"
        CREDENTIAL_FIXTURE_PATH="$<TARGET_FILE:headroom-credential-helper-fixture>"
        SSH_FIXTURE_PATH="$<TARGET_FILE:headroom-ssh-fixture>")
    add_dependencies(headroom-tests headroom-managed-server-fixture headroom-credential-helper-fixture headroom-ssh-fixture)
    qt_add_executable(headroom-ui-tests tests/test_ui.cpp)
    set_target_properties(headroom-ui-tests PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-ui-tests PRIVATE headroom-core Qt6::Test Qt6::Widgets Qt6::QuickControls2)
    target_compile_definitions(headroom-ui-tests PRIVATE SOURCE_DIR="${CMAKE_CURRENT_SOURCE_DIR}")
    qt_add_resources(headroom-ui-tests provider-icons PREFIX "/provider-icons" BASE "../shared/provider-icons" FILES
        ../shared/provider-icons/claude.svg ../shared/provider-icons/codex.svg
        ../shared/provider-icons/cursor.svg ../shared/provider-icons/grok.svg)
    add_test(NAME headroom-ui-tests COMMAND headroom-ui-tests
        -o -,txt -o "${CMAKE_CURRENT_BINARY_DIR}/headroom-ui-tests.xml,junitxml")
    set_tests_properties(headroom-ui-tests PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen;QT_QUICK_BACKEND=software")
    # DO NOT enable/implement reset action tests: they can burn a valuable reset.
    qt_add_executable(headroom-reset-prohibited-tests tests/test_reset_prohibited.cpp)
    set_target_properties(headroom-reset-prohibited-tests PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-reset-prohibited-tests PRIVATE Qt6::Core Qt6::Test)
    add_test(NAME headroom-reset-prohibited-tests COMMAND headroom-reset-prohibited-tests)
    set_tests_properties(headroom-reset-prohibited-tests PROPERTIES DISABLED TRUE)
    add_test(NAME headroom-tests COMMAND headroom-tests
        -o -,txt -o "${CMAKE_CURRENT_BINARY_DIR}/headroom-tests.xml,junitxml")
    set_tests_properties(headroom-tests PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen")
    qt_add_executable(headroom-managedserver-tests tests/test_managedserver.cpp)
    set_target_properties(headroom-managedserver-tests PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-managedserver-tests PRIVATE headroom-core Qt6::Test Qt6::Network)
    target_compile_definitions(headroom-managedserver-tests PRIVATE
        FIXTURE_PATH="$<TARGET_FILE:headroom-managed-server-fixture>"
        OWNER_PATH="$<TARGET_FILE:headroom-managed-server-owner>")
    if(APPLE)
        target_sources(headroom-managedserver-tests PRIVATE tests/macos_trust_diagnostics.cpp)
        target_link_libraries(headroom-managedserver-tests PRIVATE
            "-framework Security" "-framework CoreFoundation")
    endif()
    add_dependencies(headroom-managedserver-tests headroom-managed-server-fixture headroom-managed-server-owner)
    add_test(NAME headroom-managedserver-tests COMMAND headroom-managedserver-tests
        -o -,txt -o "${CMAKE_CURRENT_BINARY_DIR}/headroom-managedserver-tests.xml,junitxml")
    set_tests_properties(headroom-managedserver-tests PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen")
    qt_add_executable(headroom-credential-tests tests/test_credentials.cpp)
    set_target_properties(headroom-credential-tests PROPERTIES WIN32_EXECUTABLE FALSE)
    target_link_libraries(headroom-credential-tests PRIVATE headroom-core Qt6::Test Qt6::Network)
    target_compile_definitions(headroom-credential-tests PRIVATE
        CREDENTIAL_FIXTURE_PATH="$<TARGET_FILE:headroom-credential-helper-fixture>"
        SSH_FIXTURE_PATH="$<TARGET_FILE:headroom-ssh-fixture>")
    add_dependencies(headroom-credential-tests headroom-credential-helper-fixture headroom-ssh-fixture)
    add_test(NAME headroom-credential-tests COMMAND headroom-credential-tests
        -o -,txt -o "${CMAKE_CURRENT_BINARY_DIR}/headroom-credential-tests.xml,junitxml")
    set_tests_properties(headroom-credential-tests PROPERTIES ENVIRONMENT "QT_QPA_PLATFORM=offscreen")
    if(APPLE)
        foreach(tls_target IN ITEMS headroom-managed-server-fixture headroom-appinfo-tests
                headroom-tests headroom-managedserver-tests headroom-credential-tests)
            add_dependencies(${tls_target} headroom-macos-tls-fixture)
            target_include_directories(${tls_target} PRIVATE "${HEADROOM_MACOS_TLS_FIXTURE_DIR}")
        endforeach()
        # SecureTransport otherwise imports fixture server identities into the
        # login keychain, which can block unattended native test runners.
        set_property(TEST headroom-appinfo-tests headroom-tests headroom-managedserver-tests
            headroom-credential-tests APPEND PROPERTY ENVIRONMENT
            "QT_SSL_USE_TEMPORARY_KEYCHAIN=1")
    endif()
endif()

install(TARGETS headroom BUNDLE DESTINATION . RUNTIME DESTINATION bin)
if(CMAKE_SYSTEM_NAME STREQUAL "Linux")
    install(FILES headroom.desktop DESTINATION share/applications)
    install(FILES headroom.svg DESTINATION share/icons/hicolor/scalable/apps)
endif()
install(FILES update-guide.html DESTINATION share/headroom)
if(HEADROOM_PACKAGE_BUILD)
    if(WIN32)
        qt_generate_deploy_qml_app_script(
            TARGET headroom OUTPUT_SCRIPT headroom_deploy_script NO_TRANSLATIONS)
    elseif(CMAKE_SYSTEM_NAME STREQUAL "Linux")
        if(QT6_INSTALL_PREFIX MATCHES "^/usr(/|$)")
            qt_generate_deploy_qml_app_script(
                TARGET headroom OUTPUT_SCRIPT headroom_deploy_script NO_TRANSLATIONS)
        else()
            qt_generate_deploy_qml_app_script(
                TARGET headroom OUTPUT_SCRIPT headroom_deploy_script NO_TRANSLATIONS
                PRE_EXCLUDE_REGEXES "^/lib" "^/usr/lib")
        endif()
    elseif(APPLE)
        qt_generate_deploy_qml_app_script(
            TARGET headroom OUTPUT_SCRIPT headroom_deploy_script NO_TRANSLATIONS)
    endif()
    install(SCRIPT ${headroom_deploy_script})
endif()
