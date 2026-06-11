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

resource "azurerm_subnet" "vm" {
  name                 = "demo-vm"
  resource_group_name  = local.vnet_resource_group
  virtual_network_name = local.vnet_name
  address_prefixes     = ["10.0.1.0/24"]
}

resource "azurerm_network_security_group" "vm" {
  name                = "demo-vm"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name

  security_rule {
    name                       = "SSH"
    priority                   = 100
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "22"
    source_address_prefix      = "*"
    destination_address_prefix = "*"
  }

  tags = {
    Name = "demo-vm"
  }
}

resource "azurerm_subnet_network_security_group_association" "vm" {
  subnet_id                 = azurerm_subnet.vm.id
  network_security_group_id = azurerm_network_security_group.vm.id
}

resource "azurerm_public_ip" "vm" {
  name                = "demo-vm"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  allocation_method   = "Static"
  sku                 = "Standard"

  tags = {
    Name = "demo-vm"
  }
}

resource "azurerm_network_interface" "vm" {
  name                = "demo-vm"
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.vm.id
    private_ip_address_allocation = "Dynamic"
    public_ip_address_id          = azurerm_public_ip.vm.id
  }

  tags = {
    Name = "demo-vm"
  }
}

resource "azurerm_linux_virtual_machine" "main" {
  name                  = "demo"
  location              = azurerm_resource_group.main.location
  resource_group_name   = azurerm_resource_group.main.name
  size                  = "Standard_B1s"
  admin_username        = "azureuser"
  network_interface_ids = [azurerm_network_interface.vm.id]

  disable_password_authentication = true

  admin_ssh_key {
    username   = "azureuser"
    public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILWSPQrVP9waYpuXgeULzDf6qni7GWmZISD6J2Sy8Qkb demo@example.com"
  }

  os_disk {
    caching              = "ReadWrite"
    storage_account_type = "Standard_LRS"
  }

  source_image_reference {
    publisher = "Canonical"
    offer     = "0001-com-ubuntu-server-jammy"
    sku       = "22_04-lts-gen2"
    version   = "latest"
  }

  tags = {
    Name = "demo"
  }
}

output "vm_public_ip" {
  value = azurerm_public_ip.vm.ip_address
}

output "vm_private_ip" {
  value = azurerm_network_interface.vm.private_ip_address
}

output "vm_admin_username" {
  value = azurerm_linux_virtual_machine.main.admin_username
}

data "azurerm_client_config" "current" {}

resource "azurerm_postgresql_flexible_server" "main" {
  name                = "demo-db"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  version             = "16"
  sku_name            = "B_Standard_B1ms"
  storage_mb          = 32768

  public_network_access_enabled = true

  authentication {
    active_directory_auth_enabled = true
    password_auth_enabled         = false
    tenant_id                     = data.azurerm_client_config.current.tenant_id
  }

  tags = {
    Name = "demo-db"
  }
}

resource "azurerm_postgresql_flexible_server_active_directory_administrator" "main" {
  server_name         = azurerm_postgresql_flexible_server.main.name
  resource_group_name = azurerm_resource_group.main.name
  tenant_id           = data.azurerm_client_config.current.tenant_id
  object_id           = "00000000-0000-0000-0000-000000000000"
  principal_name      = "alice@example.com"
  principal_type      = "User"
}

resource "azurerm_postgresql_flexible_server_firewall_rule" "azure_services" {
  name             = "AllowAzureServices"
  server_id        = azurerm_postgresql_flexible_server.main.id
  start_ip_address = "0.0.0.0"
  end_ip_address   = "0.0.0.0"
}

output "db_fqdn" {
  value = azurerm_postgresql_flexible_server.main.fqdn
}

output "db_admin_username" {
  value = azurerm_postgresql_flexible_server_active_directory_administrator.main.principal_name
}

resource "azurerm_container_group" "main" {
  name                = "demo-aci"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  os_type             = "Linux"
  ip_address_type     = "Public"
  dns_name_label      = "demo"

  container {
    name   = "demo"
    image  = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"
    cpu    = 1
    memory = 1.5

    ports {
      port     = 80
      protocol = "TCP"
    }
  }

  tags = {
    Name = "demo"
  }
}

output "aci_ip_address" {
  value = azurerm_container_group.main.ip_address
}

output "aci_fqdn" {
  value = azurerm_container_group.main.fqdn
}
