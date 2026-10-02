# Inspect the linked executable, including CMake/linker-generated merge inputs.
execute_process(
    COMMAND "${MT}" -nologo "-inputresource:${APPLICATION};#1" "-out:${OUTPUT}"
    RESULT_VARIABLE result
    OUTPUT_VARIABLE output
    ERROR_VARIABLE error)
if(NOT result EQUAL 0)
    message(FATAL_ERROR "Cannot extract Headroom's application manifest: ${output}${error}")
endif()
file(READ "${OUTPUT}" manifest)

function(require_manifest_pattern pattern description)
    if(NOT manifest MATCHES "${pattern}")
        message(FATAL_ERROR "Embedded manifest is missing ${description}")
    endif()
endfunction()

string(REGEX MATCHALL "<assemblyIdentity[ >]" identities "${manifest}")
list(LENGTH identities identity_count)
if(NOT identity_count EQUAL 1)
    message(FATAL_ERROR "Expected one application identity, found ${identity_count}")
endif()
string(REPLACE "." "\\." version_pattern "${EXPECTED_VERSION}")
require_manifest_pattern("<assemblyIdentity[^>]*name=\"Headroom\\.Desktop\"" "Headroom branding")
require_manifest_pattern("<assemblyIdentity[^>]*version=\"${version_pattern}\"" "the package version")
require_manifest_pattern("<requestedExecutionLevel[^>]*level=\"asInvoker\"" "asInvoker execution")
require_manifest_pattern("<requestedExecutionLevel[^>]*uiAccess=\"false\"" "disabled UI access")
require_manifest_pattern("<([A-Za-z_][A-Za-z0-9_.-]*:)?supportedOS[^>]*Id=\"\\{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a\\}\"" "Windows 10/11 compatibility")
require_manifest_pattern("<dpiAwareness[^>]*>PerMonitorV2,PerMonitor</dpiAwareness>" "per-monitor DPI awareness")
require_manifest_pattern("<longPathAware[^>]*>true</longPathAware>" "long-path awareness")
message(STATUS "Verified Headroom's embedded Windows application manifest")
