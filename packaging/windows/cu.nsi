; The Windows installer: cu_<version>_windows_setup.exe, built with NSIS (runs on Linux too).
; Installs cu.exe for the current user (no administrator rights) in %LocalAppData%\Programs\cu,
; puts that on the user's PATH, and registers an uninstaller under Settings → Apps.
; One installer for both x64 and ARM64: it installs the cu.exe that fits the machine.
;
;   makensis -DVERSION=0.2.0 -DAMD64=path\to\amd64\cu.exe -DARM64=path\to\arm64\cu.exe \
;            -DOUTFILE=cu_0.2.0_windows_setup.exe packaging/windows/cu.nsi
;
; Silent install: setup.exe /S. Silent uninstall: uninstall.exe /S.

Unicode true
!include "MUI2.nsh"
!include "x64.nsh"
!include "LogicLib.nsh"

!define APP "cu"
!define LONGNAME "cu, a terminal UI for ClickUp"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\clickup-tui"
!searchparse /noerrors "${VERSION}" "" VNUM "-"  ; 0.2.0-rc1 → 0.2.0 for the file version

Name "${LONGNAME}"
OutFile "${OUTFILE}"
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\Programs\cu"
SetCompressor /SOLID lzma
BrandingText "clickup-tui ${VERSION}"

VIProductVersion "${VNUM}.0"
VIAddVersionKey "ProductName" "cu"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "FileDescription" "${LONGNAME} (installer)"
VIAddVersionKey "LegalCopyright" "MIT License"

!define MUI_ABORTWARNING
!define MUI_WELCOMEPAGE_TEXT "This installs cu ${VERSION} for your user account: no administrator rights needed.$\r$\n$\r$\ncu is added to your PATH, so after installing you can open a new terminal and run:$\r$\n$\r$\n    cu$\r$\n$\r$\nThe first time, it asks for your ClickUp API token."
!define MUI_FINISHPAGE_TEXT "cu ${VERSION} is installed.$\r$\n$\r$\nOpen a new terminal (Windows Terminal works best) and run cu. Uninstall it any time from Settings → Apps."
!define MUI_FINISHPAGE_LINK "clickup-tui on Codeberg"
!define MUI_FINISHPAGE_LINK_LOCATION "https://codeberg.org/b-wisman/clickup-tui"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "cu"
  SetOutPath "$INSTDIR"
  ${If} ${IsNativeARM64}
    File "/oname=cu.exe" "${ARM64}"
  ${Else}
    File "/oname=cu.exe" "${AMD64}"
  ${EndIf}
  WriteUninstaller "$INSTDIR\uninstall.exe"

  InitPluginsDir
  File "/oname=$PLUGINSDIR\path.ps1" "path.ps1"
  nsExec::ExecToLog 'powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$PLUGINSDIR\path.ps1" add "$INSTDIR"'
  Pop $0
  ${If} $0 != 0
    DetailPrint "Could not add $INSTDIR to PATH (exit $0): add it yourself."
  ${EndIf}

  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "${LONGNAME}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "Bertus"
  WriteRegStr HKCU "${UNINSTKEY}" "URLInfoAbout" "https://codeberg.org/b-wisman/clickup-tui"
  WriteRegStr HKCU "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\cu.exe"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  InitPluginsDir
  File "/oname=$PLUGINSDIR\path.ps1" "path.ps1"
  nsExec::ExecToLog 'powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$PLUGINSDIR\path.ps1" remove "$INSTDIR"'
  Pop $0
  Delete "$INSTDIR\cu.exe"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKCU "${UNINSTKEY}"
SectionEnd
