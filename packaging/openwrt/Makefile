include $(TOPDIR)/rules.mk

PKG_NAME:=agent-core
PKG_RELEASE:=1
PKG_LICENSE:=BSD-3-Clause
PKG_MAINTAINER:=AgentCoreGo contributors
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk

define Package/agent-core
  SECTION:=utils
  CATEGORY:=Utilities
  TITLE:=Go AI agent server
  DEPENDS:=+ca-bundle
endef

define Package/agent-core/description
 A self-hosted AI agent server with SQLite, HTTP, SSE and WebSocket.
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
endef

define Build/Compile
endef

define Package/agent-core/install
	$(INSTALL_DIR) $(1)/usr/bin $(1)/etc/agent-core
	$(INSTALL_BIN) $(CURDIR)/files/agent-core $(1)/usr/bin/agent-core
	$(INSTALL_CONF) $(CURDIR)/files/agent-core.yaml.example $(1)/etc/agent-core/agent-core.yaml.example
endef

$(eval $(call BuildPackage,agent-core))
