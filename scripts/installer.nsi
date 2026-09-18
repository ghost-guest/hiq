; hiq NSIS Installer Script
; Build: makensis scripts\installer.nsi
; Output: dist\hiq-setup.exe

!cd ".."

!define APP_NAME "hiq"
!define APP_VERSION "0.1.5"
!define APP_PUBLISHER "hiq Contributors"
!define APP_EXE "hiq.exe"
!define INSTALL_DIR "$LOCALAPPDATA\${APP_NAME}"

!include "MUI2.nsh"
!include "FileFunc.nsh"

Name "${APP_NAME} ${APP_VERSION}"
OutFile "dist\hiq-setup.exe"
InstallDir "${INSTALL_DIR}"
InstallDirRegKey HKCU "Software\${APP_NAME}" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma

!define MUI_ABORTWARNING

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

Section "Install"
    SetOutPath "$INSTDIR"

    ; Main executable
    File "desktop\build\bin\${APP_EXE}"

    ; NOTE: built-in skills (ppt-auto) are no longer copied here — they are
    ; embedded in the binary and released to $PROFILE\.hiq\skills\ on first
    ; run by the app itself. See internal/assets/.

    ; codegraph (code intelligence engine: node.exe + lib)
    ; bundled() expects: exe_dir/codegraph/bin/codegraph.cmd
    ; So unpack directly into codegraph/ (NOT codegraph/v1.0.0/)
    SetOutPath "$INSTDIR\codegraph\bin"
    File "C:\Users\13852\AppData\Local\hiq\codegraph\v1.0.0\bin\codegraph.cmd"
    SetOutPath "$INSTDIR\codegraph\lib"
    File /r "C:\Users\13852\AppData\Local\hiq\codegraph\v1.0.0\lib\*"
    File "C:\Users\13852\AppData\Local\hiq\codegraph\v1.0.0\node.exe"

    ; Registry (uninstall info)
    WriteRegStr HKCU "Software\${APP_NAME}" "InstallDir" "$INSTDIR"
    WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" \
        "DisplayName" "${APP_NAME} ${APP_VERSION}"
    WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" \
        "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
    WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" \
        "Publisher" "${APP_PUBLISHER}"
    WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" \
        "NoModify" 1
    WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" \
        "NoRepair" 1

    ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
    IntFmt $0 "0x%08X" $0
    WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}" \
        "EstimatedSize" "$0"

    ; Add to user PATH
    ReadRegStr $R0 HKCU "Environment" "Path"
    StrCpy $R0 "$R0;$INSTDIR"
    WriteRegStr HKCU "Environment" "Path" "$R0"
    SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000

    ; Start Menu
    CreateDirectory "$SMPROGRAMS\${APP_NAME}"
    CreateShortCut "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}"
    CreateShortCut "$SMPROGRAMS\${APP_NAME}\Uninstall.lnk" "$INSTDIR\uninstall.exe"

    ; Desktop shortcut
    CreateShortCut "$DESKTOP\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}"

    ; Uninstaller
    WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

Section "Uninstall"
    ; Delete files
    ; NOTE: $INSTDIR\.hiq is no longer created (skills are released to the
    ; user profile by the app). $PROFILE\.hiq is left intact — it may hold
    ; the user's own skills/data and is not owned by the installer.
    RMDir /r "$INSTDIR\codegraph"
    Delete "$INSTDIR\${APP_EXE}"
    Delete "$INSTDIR\uninstall.exe"
    RMDir "$INSTDIR"

    ; Delete shortcuts
    Delete "$DESKTOP\${APP_NAME}.lnk"
    RMDir /r "$SMPROGRAMS\${APP_NAME}"

    ; Remove from PATH (best-effort: just remove the entry)
    ReadRegStr $R0 HKCU "Environment" "Path"
    Push "$R0"
    Push ";$INSTDIR"
    Push ""
    Call un.StrRep
    Pop $R0
    WriteRegStr HKCU "Environment" "Path" "$R0"
    SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000

    ; Delete registry
    DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}"
    DeleteRegKey HKCU "Software\${APP_NAME}"
SectionEnd

Function un.StrRep
    Exch $R2
    Exch 1
    Exch $R1
    Exch 2
    Exch $R0
    Push $R3
    Push $R4
    Push $R5
    Push $R6
    Push $R7
    StrCpy $R3 ""
    StrLen $R4 $R1
    StrLen $R6 $R0
    StrLen $R7 $R2
  loop:
    StrCpy $R5 $R0 $R4 $R3
    StrCmp $R5 $R1 replace
    StrCmp $R3 $R6 done
    IntOp $R3 $R3 + 1
    Goto loop
  replace:
    StrCpy $R5 $R0 $R3
    IntOp $R3 $R3 + $R4
    StrCpy $R0 $R5$R2$R0 "" $R3
    IntOp $R3 $R3 - $R4
    IntOp $R3 $R3 + $R7
    IntOp $R6 $R6 - $R4
    IntOp $R6 $R6 + $R7
    Goto loop
  done:
    StrCpy $R2 $R0
    Pop $R7
    Pop $R6
    Pop $R5
    Pop $R4
    Pop $R3
    Pop $R0
    Pop $R1
    Exch $R2
FunctionEnd
