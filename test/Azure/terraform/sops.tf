resource "azurerm_key_vault" "this" {
  name                       = local.name_prefix
  resource_group_name        = azurerm_resource_group.this.name
  location                   = azurerm_resource_group.this.location
  tenant_id                  = data.azurerm_client_config.current.tenant_id
  sku_name                   = "standard"

  access_policy {
    tenant_id = data.azurerm_client_config.current.tenant_id
    object_id = data.azurerm_client_config.current.object_id

    key_permissions = [
      "create",
      "get",
      "list",
      "delete"
    ]

    secret_permissions = [
      "set",
      "get",
      "delete",
      "purge",
      "recover",
    ]
  }
}

resource "azurerm_key_vault_key" "sops" {
  name         = "sops-aks"
  key_vault_id = azurerm_key_vault.this.id
  key_type     = "RSA"
  key_size     = "2048"

  key_opts = [
    "decrypt",
    "encrypt",
  ]
}
