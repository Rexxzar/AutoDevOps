terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  features {}
}

resource "azurerm_resource_group" "main" {
  name     = "demo-rg"
  location = "uksouth"

  tags = {
    Name = "demo"
  }
}

output "resource_group_name" {
  value = azurerm_resource_group.main.name
}

resource "azurerm_virtual_network" "main" {
  name                = "demo-vnet"
  address_space       = ["10.0.0.0/16"]
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name

  tags = {
    Name = "demo"
  }
}

locals {
  vnet_name           = azurerm_virtual_network.main.name
  vnet_resource_group = azurerm_resource_group.main.name
}
