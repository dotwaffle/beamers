(() => {
  "use strict";

  const status = document.querySelector("[data-webauthn-status]");
  const available =
    window.isSecureContext &&
    "PublicKeyCredential" in window &&
    navigator.credentials;

  if (!available) {
    if (status) {
      status.textContent = "WebAuthn is unavailable outside a supported secure browser context.";
    }
    return;
  }

  const decode = (value) => {
    const padded = value.replace(/-/g, "+").replace(/_/g, "/").padEnd(
      Math.ceil(value.length / 4) * 4,
      "=",
    );
    return Uint8Array.from(atob(padded), (character) => character.charCodeAt(0));
  };

  const encode = (value) => {
    if (value === null) {
      return null;
    }
    let binary = "";
    for (const byte of new Uint8Array(value)) {
      binary += String.fromCharCode(byte);
    }
    return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  };

  const request = async (path, body) => {
    const response = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!response.ok) {
      throw new Error(`request failed (${response.status})`);
    }
    if (response.status === 204) {
      return null;
    }
    return response.json();
  };

  const creationOptions = (options) => {
    options.publicKey.challenge = decode(options.publicKey.challenge);
    options.publicKey.user.id = decode(options.publicKey.user.id);
    for (const credential of options.publicKey.excludeCredentials || []) {
      credential.id = decode(credential.id);
    }
    return options;
  };

  const assertionOptions = (options) => {
    options.publicKey.challenge = decode(options.publicKey.challenge);
    for (const credential of options.publicKey.allowCredentials || []) {
      credential.id = decode(credential.id);
    }
    return options;
  };

  const registrationResponse = (credential) => ({
    id: credential.id,
    rawId: encode(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: encode(credential.response.clientDataJSON),
      attestationObject: encode(credential.response.attestationObject),
      transports: credential.response.getTransports?.() || [],
    },
  });

  const assertionResponse = (credential) => ({
    id: credential.id,
    rawId: encode(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      authenticatorData: encode(credential.response.authenticatorData),
      clientDataJSON: encode(credential.response.clientDataJSON),
      signature: encode(credential.response.signature),
      userHandle: encode(credential.response.userHandle),
    },
  });

  const registration = document.querySelector("[data-webauthn-registration]");
  const register = document.querySelector("[data-webauthn-register]");
  if (registration && register) {
    registration.hidden = false;
    status.textContent = "WebAuthn is available.";
    register.addEventListener("click", async () => {
      const name = document.querySelector("[data-webauthn-name]");
      if (!name.reportValidity()) {
        return;
      }
      register.disabled = true;
      status.textContent = "Waiting for your authenticator…";
      try {
        const begin = await request("/auth/webauthn/register/begin", {});
        const credential = await navigator.credentials.create(
          creationOptions(begin.options),
        );
        await request("/auth/webauthn/register/finish", {
          ceremony_id: begin.ceremony_id,
          name: name.value,
          credential: registrationResponse(credential),
        });
        window.location.reload();
      } catch (error) {
        status.textContent = `WebAuthn registration failed: ${error.message}`;
        register.disabled = false;
      }
    });
  }

  const signInForm = document.querySelector("[data-webauthn-sign-in-form]");
  const signIn = document.querySelector("[data-webauthn-sign-in]");
  if (signInForm && signIn) {
    signIn.hidden = false;
    status.textContent = "WebAuthn is available.";
    signIn.addEventListener("click", async () => {
      const handle = signInForm.elements.handle;
      if (!handle.reportValidity()) {
        return;
      }
      signIn.disabled = true;
      status.textContent = "Waiting for your authenticator…";
      try {
        const begin = await request("/auth/webauthn/sign-in/begin", {
          handle: handle.value,
        });
        const credential = await navigator.credentials.get(
          assertionOptions(begin.options),
        );
        await request("/auth/webauthn/sign-in/finish", {
          ceremony_id: begin.ceremony_id,
          credential: assertionResponse(credential),
        });
        window.location.assign(signInForm.dataset.returnTo || "/");
      } catch (error) {
        status.textContent = `WebAuthn sign-in failed: ${error.message}`;
        signIn.disabled = false;
      }
    });
  }
})();
