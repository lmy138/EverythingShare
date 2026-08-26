[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$webRoot = Join-Path $projectRoot 'web'

Add-Type -AssemblyName System.Drawing.Common
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

public static class WindowsShellIcon {
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct SHFILEINFO {
        public IntPtr hIcon;
        public int iIcon;
        public uint dwAttributes;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 260)] public string szDisplayName;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 80)] public string szTypeName;
    }

    [DllImport("shell32.dll", CharSet = CharSet.Unicode)]
    private static extern IntPtr SHGetFileInfo(string pszPath, uint dwFileAttributes, ref SHFILEINFO psfi, uint cbFileInfo, uint uFlags);

    [DllImport("user32.dll")]
    private static extern bool DestroyIcon(IntPtr hIcon);

    public static System.Drawing.Icon Get(string name, bool folder) {
        const uint SHGFI_ICON = 0x000000100;
        const uint SHGFI_LARGEICON = 0x000000000;
        const uint SHGFI_USEFILEATTRIBUTES = 0x000000010;
        const uint FILE_ATTRIBUTE_DIRECTORY = 0x00000010;
        const uint FILE_ATTRIBUTE_NORMAL = 0x00000080;
        SHFILEINFO info = new SHFILEINFO();
        uint attributes = folder ? FILE_ATTRIBUTE_DIRECTORY : FILE_ATTRIBUTE_NORMAL;
        IntPtr result = SHGetFileInfo(name, attributes, ref info, (uint)Marshal.SizeOf(info), SHGFI_ICON | SHGFI_LARGEICON | SHGFI_USEFILEATTRIBUTES);
        if (result == IntPtr.Zero || info.hIcon == IntPtr.Zero) throw new InvalidOperationException("Windows Shell did not return an icon for " + name);
        try {
            return (System.Drawing.Icon)System.Drawing.Icon.FromHandle(info.hIcon).Clone();
        } finally {
            DestroyIcon(info.hIcon);
        }
    }
}
'@ -ReferencedAssemblies System.Drawing.Common

$extensions = @(
    '7z','aac','apk','appx','avi','bat','bmp','bz2','c','cab','cc','cmd','com','cpp','css','csv',
    'dmg','doc','docm','docx','eot','exe','flac','flv','gif','go','gz','h','heic','html','ico','img',
    'iso','java','jpeg','jpg','js','json','jsx','m4a','m4v','md','mkv','mov','mp3','mp4','mpeg','mpg',
    'msi','odp','ods','odt','ogg','opus','otf','pdf','php','png','ppt','pptm','pptx','ps1','py','rar',
    'raw','rb','rs','rtf','scss','sh','sql','svg','tar','tgz','tif','tiff','ts','tsx','ttf','txt','vhd',
    'vhdx','vue','wav','webm','webp','wma','wmv','woff','woff2','xls','xlsb','xlsm','xlsx','xml','xz',
    'yaml','yml','zip'
)

function Export-ShellIcon([string]$name, [string]$outputName, [bool]$folder) {
    $target = Join-Path $webRoot $outputName
    $icon = [WindowsShellIcon]::Get($name, $folder)
    try {
        $bitmap = $icon.ToBitmap()
        try { $bitmap.Save($target, [Drawing.Imaging.ImageFormat]::Png) }
        finally { $bitmap.Dispose() }
    } finally { $icon.Dispose() }
}

Export-ShellIcon 'folder' 'system-icon-folder.png' $true
Export-ShellIcon 'file.unknown' 'system-icon-file.png' $false
foreach ($extension in $extensions) {
    Export-ShellIcon "file.$extension" "system-icon-$extension.png" $false
}

Write-Host "Exported $($extensions.Count + 2) Windows Shell icons to $webRoot"
