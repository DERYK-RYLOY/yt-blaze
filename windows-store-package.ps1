# Requires: MSIX Packaging Tool, signtool, certificate .pfx
$certPath = "C:\certs\ytblaze.pfx"
$certPass = "YOUR_CERT_PASSWORD"
$appExe = "build\bin\YT Blaze.exe"
$installer = "build\bin\ytblaze-desktop-amd64-installer.exe"

# Sign installer
& "C:\Program Files (x86)\Windows Kits\10\bin\10.0.22621.0\x64\signtool.exe" sign /f $certPath /p $certPass /tr http://timestamp.digicert.com /td sha256 /fd sha256 $installer

# Create MSIX using MSIX Packaging Tool (manual step)
# 1. Open MSIX Packaging Tool
# 2. Choose "Create a new MSIX package"
# 3. Add installer as setup, set InstallLocation
# 4. Sign with same cert
# 5. Output: YTBlaze.msix

Write-Host "Package signed. Upload .msix and .msixbundle to Partner Center."
