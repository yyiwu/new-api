param(
    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string]$Version,
    [string]$Image = 'new-api',
    [string]$BuildDate = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
)

[System.IO.File]::WriteAllText((Join-Path $PSScriptRoot 'VERSION'), "$Version`n")

docker build `
    --label "org.opencontainers.image.version=$Version" `
    --label "org.opencontainers.image.created=$BuildDate" `
    -t "${Image}:$Version" `
    $PSScriptRoot

if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
