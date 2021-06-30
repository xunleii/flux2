terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "2.67.0"
    }
  }
}

provider "azurerm" {
  features {}
}

resource "random_pet" "prefix" {}

locals {
  name_prefix = "${random_pet.prefix.id}-e2e"
}

data "azurerm_client_config" "current" {}

resource "azurerm_resource_group" "this" {
  name     = "${local.name_prefix}-rg"
  location = "West Europe"

  tags = {
    environment = "e2e"
  }
}
