FROM quay.io/jupyter/pytorch-notebook:cuda12-ubuntu-24.04
COPY jupyter-ai.yml environment.yml

#RUN conda update -n base -c conda-forge conda && conda env update --file environment.yml
RUN conda update --all --solver=classic -n base -c conda-forge conda && \
    conda env update --file environment.yml


USER root

COPY apt.txt apt.txt
RUN apt-get update -qq && xargs sudo apt-get -y install < apt.txt

RUN curl -L https://ollama.com/download/ollama-linux-amd64.tgz -o ollama-linux-amd64.tgz && \
  tar -C /usr -xzf ollama-linux-amd64.tgz

RUN curl https://dl.min.io/client/mc/release/linux-amd64/mc -o /usr/local/bin/mc && chmod +x /usr/local/bin/mc

RUN curl -fsSL https://code-server.dev/install.sh | sh && rm -rf .cache

RUN git config --system pull.rebase false && \
    git config --system credential.helper 'cache --timeout=30000' && \
    echo '"\e[5~": history-search-backward' >> /etc/inputrc && \
    echo '"\e[6~": history-search-forward' >> /etc/inputrc

USER ${NB_USER}

