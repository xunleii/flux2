resource "azurerm_eventhub_namespace" "this" {
  name = "${local.name_prefix}-ehns"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  sku                 = "Standard"
  capacity            = 1
}


resource "azurerm_eventhub" "this" {
  name                = "${local.name_prefix}-eh"
  namespace_name      = azurerm_eventhub_namespace.this.name
  resource_group_name = azurerm_resource_group.this.name
  partition_count     = 1
  message_retention   = 1
}
