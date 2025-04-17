// Global variable for attached resources
let attachedResources = [];

function showServerModal(serverName) {
    const modalText = document.getElementById('serverModalText');
    modalText.textContent = `Server: ${serverName}`;
    const modal = new bootstrap.Modal(document.getElementById('serverModal'));
    modal.show();
}

function handleKeyPress(event, formID) {
    if (event.key === 'Enter') {
        if (!event.shiftKey) {
            event.preventDefault();
            htmx.trigger(formID, 'submit');
        }
    }
    // Auto-expand height
    adjustHeight(event.target);
}

function adjustHeight(element) {
    element.style.height = 'auto';
    element.style.height = (element.scrollHeight) + 'px';
}

function showPromptModal(promptIndex) {
    const promptData = promptsList[promptIndex];
    if (!promptData) {
        console.error('Prompt not found at index:', promptIndex);
        return;
    }
    
    // Set the modal title and description
    document.getElementById('promptModalLabel').textContent = `Prompt: ${promptData.name}`;
    const descElem = document.getElementById('promptDescription');
    descElem.textContent = promptData.description || '';
    
    // Get the form element where we'll add the inputs
    const argContainer = document.getElementById('promptArguments');
    argContainer.innerHTML = '';
    
    // Create input fields for each argument
    if (promptData.arguments && promptData.arguments.length > 0) {
        promptData.arguments.forEach(arg => {
            const formGroup = document.createElement('div');
            formGroup.className = 'mb-3';
            
            const label = document.createElement('label');
            label.htmlFor = `arg-${arg.name}`;
            label.className = 'form-label';
            label.textContent = arg.name;
            if (arg.required) {
                label.textContent += ' *';
            }
            
            const input = document.createElement('input');
            input.type = 'text';
            input.className = 'form-control';
            input.id = `arg-${arg.name}`;
            input.name = arg.name;
            input.required = arg.required;
            
            formGroup.appendChild(label);
            formGroup.appendChild(input);
            
            if (arg.description) {
                const helpText = document.createElement('div');
                helpText.className = 'form-text';
                helpText.textContent = arg.description;
                formGroup.appendChild(helpText);
            }
            
            argContainer.appendChild(formGroup);
        });
    } else {
        // If there are no arguments, show a message
        const noArgsMsg = document.createElement('p');
        noArgsMsg.textContent = 'This prompt has no arguments.';
        argContainer.appendChild(noArgsMsg);
    }
    
    // Set up the "Use Prompt" button handler
    document.getElementById('usePromptBtn').onclick = function() {
        // Collect prompt data and arguments
        const args = {};
        const promptName = promptData.name;
        
        if (promptData.arguments) {
            promptData.arguments.forEach(arg => {
                const input = document.getElementById(`arg-${arg.name}`);
                if (input) {
                    args[arg.name] = input.value;
                }
            });
        }
        
        // Determine which form to use
        const isWelcomePage = document.getElementById('chat-form-welcome') !== null;
        const formId = isWelcomePage ? 'chat-form-welcome' : 'chat-form-chatbox';
        const form = document.getElementById(formId);

        // Get the textarea and temporarily remove required attribute
        const textarea = form.querySelector('textarea[name="message"]');
        textarea.removeAttribute('required');

        // Clear the message field and set prompt data
        textarea.value = '';
        form.querySelector('input[name="prompt_name"]').value = promptName;
        form.querySelector('input[name="prompt_args"]').value = JSON.stringify(args);
        
        // Set up a one-time event listener for after the request completes
        form.addEventListener('htmx:afterRequest', function afterRequest() {
            // Restore the required attribute and clear prompt fields
            textarea.setAttribute('required', '');
            form.querySelector('input[name="prompt_name"]').value = '';
            form.querySelector('input[name="prompt_args"]').value = '';
            
            // Remove this event listener to prevent it from firing on future requests
            form.removeEventListener('htmx:afterRequest', afterRequest);
        }, { once: true });
        
        // Submit the form
        htmx.trigger(form, 'submit');
        
        // Close the modal
        bootstrap.Modal.getInstance(document.getElementById('promptModal')).hide();
    };
    
    // Show the modal
    new bootstrap.Modal(document.getElementById('promptModal')).show();
}

function attachResource(resource) {
    // Add resource to the tracking array if not already present
    if (!attachedResources.some(r => r.uri === resource.uri)) {
        attachedResources.push(resource);
        updateAttachedResourcesDisplay();
    }
}

function removeResource(uri) {
    // Remove the resource from the tracking array
    attachedResources = attachedResources.filter(r => r.uri !== uri);
    updateAttachedResourcesDisplay();
}

function clearAttachedResources() {
    attachedResources = [];
    updateAttachedResourcesDisplay();
}

function updateAttachedResourcesDisplay() {
    // Get the container element
    const container = document.getElementById('attached-resources-container');
    const list = document.getElementById('attached-resources-list');
    
    if (!container || !list) return;
    
    // Show/hide the container based on whether there are resources
    container.style.display = attachedResources.length > 0 ? 'block' : 'none';
    
    // Clear the list
    list.innerHTML = '';
    
    // Add badges for each resource
    attachedResources.forEach(resource => {
        const badge = document.createElement('div');

        // Create display text with name and URI
        let displayText = resource.name || 'Resource';
        if (resource.uri) {
            displayText += ` (${resource.uri})`;
        }

        badge.className = 'badge bg-secondary text-white d-flex align-items-center p-2 me-1 mb-1';
        badge.innerHTML = `
            <span class="me-2 text-truncate" style="max-width: 250px;" title="${displayText}">${displayText}</span>
            <button type="button" class="btn-close btn-close-white btn-close-sm flex-shrink-0" 
                    aria-label="Remove" onclick="removeResource('${resource.uri}')"></button>
        `;
        list.appendChild(badge);
    });
    
    // Update the hidden form input
    const isWelcomePage = document.getElementById('chat-form-welcome') !== null;
    const formId = isWelcomePage ? 'chat-form-welcome' : 'chat-form-chatbox';
    const form = document.getElementById(formId);
    
    if (form) {
        const input = form.querySelector('input[name="attached_resources"]');
        if (input) {
            input.value = JSON.stringify(attachedResources.map(r => r.uri));
        }
    }
}

function showResourceModal(resourceIndex) {
    const resourceData = resourcesList[resourceIndex];
    if (!resourceData) {
        console.error('Resource not found at index:', resourceIndex);
        return;
    }
    
    // Set the modal content
    document.getElementById('resourceName').textContent = resourceData.name || 'Unnamed Resource';
    document.getElementById('resourceDescription').textContent = resourceData.description || 'No description available';
    document.getElementById('resourceUri').textContent = resourceData.uri || '';
    document.getElementById('resourceMimeType').textContent = resourceData.mimeType || 'Unknown';

    // Set up the "Use Resource" button handler
    document.getElementById('useResourceBtn').onclick = function() {
        attachResource(resourceData);
        bootstrap.Modal.getInstance(document.getElementById('resourceModal')).hide();
    };
    
    // Show the modal
    new bootstrap.Modal(document.getElementById('resourceModal')).show();
}

document.addEventListener('DOMContentLoaded', function() {
    // Fix any trailing commas in arrays
    for (let i = 0; i < promptsList.length; i++) {
        if (promptsList[i].arguments && promptsList[i].arguments.length > 0) {
            // Remove any undefined items that may have been created by trailing commas
            promptsList[i].arguments = promptsList[i].arguments.filter(item => item !== undefined);
        }
    }

    // Clean up the resources list
    if (typeof resourcesList !== 'undefined') {
        resourcesList = resourcesList.filter(item => item !== undefined);
    }
});
