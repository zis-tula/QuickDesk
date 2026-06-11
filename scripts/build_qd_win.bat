@echo off

echo=
echo=
echo ---------------------------------------------------------------
echo check ENV
echo ---------------------------------------------------------------

:: example: C:\QtPro\6.8.4
if "%ENV_QT_PATH%"=="" set ENV_QT_PATH=C:\QtPro\6.8.4
echo ENV_QT_PATH %ENV_QT_PATH%

:: get script absolute path
set script_path=%~dp0
:: enter script directory, as it affects the working directory of programs executed in the script
set old_cd=%cd%
cd /d %~dp0

:: declare startup parameters and default values
SETLOCAL EnableDelayedExpansion
set cpu_mode=x64
set build_mode=Release
set clean_output=false
set errno=1

echo=
echo=
echo ---------------------------------------------------------------
echo parse arguments
echo ---------------------------------------------------------------

:: iterate all arguments
:: note: %1 always represents the current first argument, shift moves all arguments one position to the left
:: example: build_qd_win.bat release clean -> %1=release, after shift -> %1=clean
:parse_args
if "%1"=="" goto args_done

REM check build type (case insensitive)
if /i "%1"=="debug" set build_mode=Debug
if /i "%1"=="release" set build_mode=Release
if /i "%1"=="minsizerel" set build_mode=MinSizeRel
if /i "%1"=="relwithdebinfo" set build_mode=RelWithDebInfo

REM check if clean is needed
if /i "%1"=="clean" set clean_output=true

shift
goto parse_args
:args_done

echo [*] build mode: %build_mode%
echo [*] clean output: %clean_output%
echo=

set cpu_mode=x64
set cmake_vs_build_mode=x64
set qt_cmake_path=%ENV_QT_PATH%\msvc2022_64

echo=
echo Qt cmake path: %qt_cmake_path%

echo=
echo=
echo ---------------------------------------------------------------
echo begin CMake build
echo ---------------------------------------------------------------

:: handle output directory
set output_path=%script_path%..\output
if "!clean_output!"=="true" (
    if exist "%output_path%" (
        echo [*] cleaning output dir: %output_path%
        rmdir /q /s "%output_path%"
    )
) else (
    echo [*] keeping output dir: %output_path%
)

:: handle temp directory
set temp_path=%script_path%..\build-temp
if "!clean_output!"=="true" (
    if exist "%temp_path%" (
        echo [*] cleaning temp dir: %temp_path%
        rmdir /q /s "%temp_path%"
    )
) else (
    echo [*] keeping temp dir ^(incremental build^): %temp_path%
)

:: ensure temp directory exists
if not exist "%temp_path%" (
    md "%temp_path%"
)
cd /d "%temp_path%"

:: Support CMAKE_GENERATOR override (for CI with Ninja)
if defined CMAKE_GENERATOR (
    if not "%CMAKE_GENERATOR%"=="" (
        set cmake_generator=-G "%CMAKE_GENERATOR%"
        if /i "%CMAKE_GENERATOR%"=="Ninja" set cmake_vs_build_mode=
    ) else (
        set cmake_generator=-G "Visual Studio 17 2022" -A %cmake_vs_build_mode%
    )
) else (
    set cmake_generator=-G "Visual Studio 17 2022" -A %cmake_vs_build_mode%
)

set cmake_params=-DCMAKE_PREFIX_PATH=%qt_cmake_path% -DCMAKE_BUILD_TYPE=%build_mode% %cmake_generator%

if defined ENV_QUICKDESK_API_KEY (
    if not "%ENV_QUICKDESK_API_KEY%"=="" (
        set cmake_params=%cmake_params% -DQUICKDESK_API_KEY=%ENV_QUICKDESK_API_KEY%
        echo [*] QUICKDESK_API_KEY: configured
    ) else (
        echo [*] QUICKDESK_API_KEY: not set ^(open-source build^)
    )
) else (
    echo [*] QUICKDESK_API_KEY: not set ^(open-source build^)
)

echo [*] CMake params: %cmake_params%
echo=

cmake %cmake_params% ../
if not %errorlevel%==0 (
    echo [!] CMake configure failed
    goto return
)

echo=
echo [*] building...
cmd /c cmake --build . --config %build_mode% --parallel
if not %errorlevel%==0 (
    echo [!] CMake build failed
    goto return
)

echo=
echo=
echo ---------------------------------------------------------------
echo [*] build finished!
echo ---------------------------------------------------------------

set errno=0

:return
cd %old_cd%
exit /B %errno%

ENDLOCAL
