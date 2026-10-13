$ErrorActionPreference = 'Stop'

$source = Join-Path $PSScriptRoot 'guia-de-estudo.html'
$target = Join-Path $PSScriptRoot 'guia-de-estudo.pdf'

$browser = @(
    "$env:ProgramFiles\Google\Chrome\Application\chrome.exe",
    "${env:ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe",
    "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe",
    "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe"
) | Where-Object { Test-Path $_ } | Select-Object -First 1

if (-not $browser) {
    throw 'Chrome or Edge is required to generate the PDF.'
}

if (Test-Path $target) {
    Remove-Item $target
}

$arguments = @(
    '--headless=new',
    '--disable-gpu',
    '--no-pdf-header-footer',
    "--print-to-pdf=`"$target`"",
    "`"$(([System.Uri]$source).AbsoluteUri)`""
)
Start-Process -FilePath $browser -ArgumentList $arguments -Wait

if (-not (Test-Path $target)) {
    throw 'The browser finished without producing the PDF.'
}

Write-Output "Generated $target"
